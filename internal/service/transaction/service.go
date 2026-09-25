package transaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
	"aura-saku/internal/service/account"
	"aura-saku/internal/service/fx"
)

// Service owns persistence of transactions, including multi-currency
// conversion and the /undo flow.
type Service struct {
	db    *gorm.DB
	fxSvc fx.Service
}

func NewService(db *gorm.DB, fxSvc fx.Service) *Service {
	return &Service{db: db, fxSvc: fxSvc}
}

// CreateFromParsed persists a Transaction from an AI-parsed result: it
// resolves (or creates) the Category, fetches the exchange rate to the
// user's default currency, computes ConvertedAmount, and records the insert
// in the same DB transaction as an UndoHistory("created") snapshot.
func (s *Service) CreateFromParsed(ctx context.Context, userID uint, defaultCurrency string, parsed *ai.ParsedTransaction, source models.TransactionSource, rawInput string) (*models.Transaction, error) {
	categoryID, err := s.resolveCategory(ctx, parsed.CategoryName)
	if err != nil {
		return nil, fmt.Errorf("resolve category: %w", err)
	}

	acc, err := account.GetOrCreateDefault(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}

	occurredAt := resolveOccurredAt(parsed.OccurredAtHint)

	rate, err := s.fxSvc.GetExchangeRate(ctx, parsed.Currency, defaultCurrency, occurredAt)
	if err != nil {
		return nil, fmt.Errorf("resolve exchange rate: %w", err)
	}

	tx := &models.Transaction{
		UserID:           userID,
		AccountID:        &acc.ID,
		CategoryID:       categoryID,
		Type:             models.TransactionType(parsed.Type),
		Description:      parsed.Description,
		Bucket:           resolveBucket(parsed.Bucket),
		OriginalAmount:   parsed.Amount,
		OriginalCurrency: strings.ToUpper(parsed.Currency),
		ExchangeRate:     rate,
		ConvertedAmount:  parsed.Amount * rate,
		Source:           source,
		RawInput:         rawInput,
		OccurredAt:       occurredAt,
	}

	err = s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		// Omit the User association: Transaction.User is a value-type belongs-to
		// field left at its zero value here, and without Omit, GORM's default
		// association auto-save could attempt to upsert an empty User row.
		if err := txDB.Omit("User").Create(tx).Error; err != nil {
			return fmt.Errorf("insert transaction: %w", err)
		}
		return writeUndoHistory(txDB, userID, tx, "created")
	})
	if err != nil {
		return nil, err
	}

	return tx, nil
}

// CreateAdjustment persists a system-generated balance adjustment
// transaction (used by reconciliation.Service when /sync finds a
// discrepancy between the user's actual and calculated balance). It always
// records in the user's default currency, so no FX conversion applies.
// Positive amount records an income adjustment (unrecorded money in);
// negative records an expense adjustment (unrecorded money out).
func (s *Service) CreateAdjustment(ctx context.Context, userID uint, amount float64, currency, description string) (*models.Transaction, error) {
	acc, err := account.GetOrCreateDefault(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}

	txType := models.TxIncome
	absAmount := amount
	if amount < 0 {
		txType = models.TxExpense
		absAmount = -amount
	}

	tx := &models.Transaction{
		UserID:           userID,
		AccountID:        &acc.ID,
		Type:             txType,
		Description:      description,
		OriginalAmount:   absAmount,
		OriginalCurrency: strings.ToUpper(currency),
		ExchangeRate:     1,
		ConvertedAmount:  absAmount,
		Source:           models.SourceSync,
		RawInput:         description,
		OccurredAt:       time.Now().UTC(),
	}

	err = s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Omit("User").Create(tx).Error; err != nil {
			return fmt.Errorf("insert adjustment transaction: %w", err)
		}
		return writeUndoHistory(txDB, userID, tx, "created")
	})
	if err != nil {
		return nil, err
	}

	return tx, nil
}

// MostRecent returns the user's most recent transaction without modifying
// it. Exported (rather than folded into UndoLast alone) so the correction
// flow below can capture *which* transaction to revert before creating the
// corrected replacement — calling UndoLast after that create would
// otherwise resolve "most recent" to the new transaction itself.
func (s *Service) MostRecent(ctx context.Context, userID uint) (*models.Transaction, error) {
	var tx models.Transaction
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		First(&tx).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrNoUndoableTransaction
		}
		return nil, fmt.Errorf("find last transaction: %w", err)
	}
	return &tx, nil
}

// UndoLast soft-deletes the most recent transaction for a user and records
// an UndoHistory("undone") snapshot in the same DB transaction.
func (s *Service) UndoLast(ctx context.Context, userID uint) (*models.Transaction, error) {
	tx, err := s.MostRecent(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.undoTransaction(ctx, userID, tx)
}

// CorrectLast replaces the user's most recent transaction with a corrected
// one described by parsed (ai.IntentCorrect — "eh maaf, harusnya 1jt deh,
// typo"): it creates the new transaction first and only reverts the
// previous one on success. This ordering is deliberate — if creation fails,
// the user's original (uncorrected but real) transaction is left intact
// rather than silently deleted with nothing to replace it.
//
// Returns the newly created transaction and, separately, the transaction
// that was reverted (nil if there was nothing to correct — e.g. this was
// the user's very first message — which is not an error: recording it
// fresh is still the right outcome). If creation itself fails, created is
// nil and err explains why. If creation succeeds but the revert step fails,
// created is still returned (the correction was recorded) alongside a
// non-nil err describing the revert failure, so the caller can decide how
// to report a partial outcome instead of discarding a real transaction.
func (s *Service) CorrectLast(ctx context.Context, userID uint, defaultCurrency string, parsed *ai.ParsedTransaction, source models.TransactionSource, rawInput string) (created *models.Transaction, undone *models.Transaction, err error) {
	previous, err := s.MostRecent(ctx, userID)
	if err != nil && !errors.Is(err, apperror.ErrNoUndoableTransaction) {
		return nil, nil, fmt.Errorf("find transaction to correct: %w", err)
	}
	// previous is nil here exactly when err is ErrNoUndoableTransaction —
	// nothing to correct, but we still record the new transaction below.

	created, err = s.CreateFromParsed(ctx, userID, defaultCurrency, parsed, source, rawInput)
	if err != nil {
		return nil, nil, err
	}

	if previous == nil {
		return created, nil, nil
	}

	undone, err = s.undoTransaction(ctx, userID, previous)
	if err != nil {
		return created, nil, fmt.Errorf("undo previous transaction after correction: %w", err)
	}
	return created, undone, nil
}

// undoTransaction soft-deletes tx and records an UndoHistory("undone")
// snapshot in the same DB transaction — shared by UndoLast (targets the
// user's most recent transaction) and CorrectLast (targets a transaction
// captured before a new one was created).
func (s *Service) undoTransaction(ctx context.Context, userID uint, tx *models.Transaction) (*models.Transaction, error) {
	err := s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Delete(tx).Error; err != nil {
			return fmt.Errorf("soft-delete transaction: %w", err)
		}
		return writeUndoHistory(txDB, userID, tx, "undone")
	})
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// defaultCategoryBuckets seeds newly-created categories with a best-effort
// 50/30/20 bucket guess from common category names, so budget.Service has
// something to group by without requiring a separate category-management
// feature. This is a keyword heuristic, not a classifier — categories not
// listed here default to BucketNone (counted in totals but not toward any
// Needs/Wants/Savings limit) until a smarter classification exists.
var defaultCategoryBuckets = map[string]models.BudgetBucket{
	"food":           models.BucketNeeds,
	"groceries":      models.BucketNeeds,
	"rent":           models.BucketNeeds,
	"housing":        models.BucketNeeds,
	"utilities":      models.BucketNeeds,
	"transport":      models.BucketNeeds,
	"transportation": models.BucketNeeds,
	"healthcare":     models.BucketNeeds,
	"health":         models.BucketNeeds,
	"insurance":      models.BucketNeeds,
	"education":      models.BucketNeeds,
	"bills":          models.BucketNeeds,

	"entertainment": models.BucketWants,
	"shopping":      models.BucketWants,
	"dining":        models.BucketWants,
	"restaurant":    models.BucketWants,
	"travel":        models.BucketWants,
	"hobby":         models.BucketWants,
	"subscription":  models.BucketWants,
	"coffee":        models.BucketWants,

	"savings":        models.BucketSavings,
	"investment":     models.BucketSavings,
	"debt":           models.BucketSavings,
	"emergency fund": models.BucketSavings,
}

func (s *Service) resolveCategory(ctx context.Context, categoryName string) (*uint, error) {
	name := strings.ToLower(strings.TrimSpace(categoryName))
	if name == "" {
		return nil, nil
	}

	bucket := models.BucketNone
	if b, ok := defaultCategoryBuckets[name]; ok {
		bucket = b
	}

	var category models.Category
	err := s.db.WithContext(ctx).
		Where(models.Category{Name: name}).
		Attrs(models.Category{Bucket: bucket, IsSystem: false}).
		FirstOrCreate(&category).Error
	if err != nil {
		return nil, err
	}

	// Self-heal: Attrs only applies when FirstOrCreate inserts a new row, so
	// a category created before defaultCategoryBuckets had an entry for it
	// (or before this heuristic existed at all) would otherwise stay
	// BucketNone forever even once we know better.
	if bucket != models.BucketNone && category.Bucket == models.BucketNone {
		if err := s.db.WithContext(ctx).Model(&models.Category{}).Where("id = ?", category.ID).Update("bucket", bucket).Error; err != nil {
			return nil, err
		}
		category.Bucket = bucket
	}

	return &category.ID, nil
}

// resolveBucket converts the AI's raw bucket classification into a
// validated pointer for Transaction.Bucket. Empty or unrecognized input
// returns nil ("no override") rather than an error — same defensive
// philosophy as resolveOccurredAt's unrecognized-hint fallback just below:
// a cosmetic miss on a nice-to-have field shouldn't fail the whole
// transaction. nil falls back to the category's own default bucket (see
// budget.Service.sumSpendByBucket).
func resolveBucket(raw string) *models.BudgetBucket {
	b := models.BudgetBucket(strings.ToLower(strings.TrimSpace(raw)))
	switch b {
	case models.BucketNeeds, models.BucketWants, models.BucketSavings, models.BucketNone:
		return &b
	default:
		return nil
	}
}

// resolveOccurredAt interprets the AI's occurred-at hint into a concrete
// timestamp. The prompt (see internal/ai/prompt) tells the model today's
// date and asks it to resolve any relative language ("yesterday", "last
// monday", "3 days ago") into a strict ISO 8601 date itself — offloading
// that reasoning to the model is far more robust than pattern-matching
// arbitrary natural language in Go. The legacy word cases below remain as a
// defensive fallback in case a model doesn't follow the ISO-date instruction.
func resolveOccurredAt(hint string) time.Time {
	now := time.Now().UTC()
	hint = strings.ToLower(strings.TrimSpace(hint))

	switch hint {
	case "", "today", "now":
		return now
	case "yesterday":
		return now.AddDate(0, 0, -1)
	}

	if parsed, err := time.Parse("2006-01-02", hint); err == nil {
		if parsed.Year() == now.Year() && parsed.YearDay() == now.YearDay() {
			// Same-day entry: preserve actual time-of-day rather than
			// truncating to midnight, so ordering (e.g. /undo "most recent")
			// stays accurate.
			return now
		}
		return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, time.UTC)
	}

	// Unrecognized hint (model didn't follow the ISO-date instruction) —
	// default to now rather than failing the whole transaction over a
	// cosmetic date-parsing miss.
	return now
}

func writeUndoHistory(db *gorm.DB, userID uint, tx *models.Transaction, action string) error {
	snapshot, err := json.Marshal(tx)
	if err != nil {
		return fmt.Errorf("marshal transaction snapshot: %w", err)
	}

	history := &models.UndoHistory{
		UserID:        userID,
		TransactionID: tx.ID,
		Action:        action,
		SnapshotJSON:  string(snapshot),
	}
	return db.Create(history).Error
}
