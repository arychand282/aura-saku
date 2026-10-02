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

	acc, err := account.GetOrCreateByName(ctx, s.db, userID, parsed.AccountName)
	if err != nil {
		return nil, err
	}

	occurredAt := resolveOccurredAt(parsed.OccurredAtHint)

	// The AI is instructed to always fill in currency (defaulting to
	// defaultCurrency when the user doesn't mention one explicitly — see
	// tool_schema.go's "currency" field description), but tool-calling
	// compliance isn't guaranteed — an empty parsed.Currency has been
	// observed in practice. Falling back here rather than trusting the
	// model keeps a blank value from ever reaching GetExchangeRate (which
	// would otherwise try to look up a rate for an empty currency code) or
	// being persisted as the transaction's OriginalCurrency.
	currency := strings.ToUpper(parsed.Currency)
	if currency == "" {
		currency = strings.ToUpper(defaultCurrency)
	}

	rate, err := s.fxSvc.GetExchangeRate(ctx, currency, defaultCurrency, occurredAt)
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
		OriginalCurrency: currency,
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

// CreateTransfer moves money between two of the user's OWN accounts (e.g.
// "pindahin 200rb dari BCA ke Gopay" — ai.IntentAccountTransfer/move_money),
// as opposed to a plain Type=transfer record_transaction entry (money sent
// to someone else, which stays on a single account unchanged). It records
// TWO linked rows — an "out" leg on fromAccountName's account, an "in" leg
// on toAccountName's — both resolved/created by name the same way
// CreateFromParsed resolves a category (see account.GetOrCreateByName).
// Both legs share the exact same OriginalAmount/OriginalCurrency/
// ExchangeRate/ConvertedAmount, which is what makes them net to exactly
// zero across the user's total balance (see account.TotalBalance) while
// correctly shifting each individual account's own balance.
func (s *Service) CreateTransfer(ctx context.Context, userID uint, defaultCurrency, fromAccountName, toAccountName string, amount float64, currency, description, occurredAtHint string) (fromTx, toTx *models.Transaction, err error) {
	fromAcc, err := account.GetOrCreateByName(ctx, s.db, userID, fromAccountName)
	if err != nil {
		return nil, nil, err
	}
	toAcc, err := account.GetOrCreateByName(ctx, s.db, userID, toAccountName)
	if err != nil {
		return nil, nil, err
	}
	if fromAcc.ID == toAcc.ID {
		return nil, nil, apperror.ErrSameAccountTransfer
	}

	occurredAt := resolveOccurredAt(occurredAtHint)

	currency = strings.ToUpper(currency)
	if currency == "" {
		currency = strings.ToUpper(defaultCurrency)
	}

	rate, err := s.fxSvc.GetExchangeRate(ctx, currency, defaultCurrency, occurredAt)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve exchange rate: %w", err)
	}
	convertedAmount := amount * rate

	outLeg := models.TransferLegOut
	inLeg := models.TransferLegIn
	out := &models.Transaction{
		UserID:           userID,
		AccountID:        &fromAcc.ID,
		TransferLeg:      &outLeg,
		Type:             models.TxTransfer,
		Description:      description,
		OriginalAmount:   amount,
		OriginalCurrency: currency,
		ExchangeRate:     rate,
		ConvertedAmount:  convertedAmount,
		Source:           models.SourceText,
		RawInput:         description,
		OccurredAt:       occurredAt,
	}
	in := &models.Transaction{
		UserID:           userID,
		AccountID:        &toAcc.ID,
		TransferLeg:      &inLeg,
		Type:             models.TxTransfer,
		Description:      description,
		OriginalAmount:   amount,
		OriginalCurrency: currency,
		ExchangeRate:     rate,
		ConvertedAmount:  convertedAmount,
		Source:           models.SourceText,
		RawInput:         description,
		OccurredAt:       occurredAt,
	}

	err = s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Omit("User").Create(out).Error; err != nil {
			return fmt.Errorf("insert transfer out-leg: %w", err)
		}
		in.LinkedTransactionID = &out.ID
		if err := txDB.Omit("User").Create(in).Error; err != nil {
			return fmt.Errorf("insert transfer in-leg: %w", err)
		}
		out.LinkedTransactionID = &in.ID
		if err := txDB.Model(out).Update("linked_transaction_id", in.ID).Error; err != nil {
			return fmt.Errorf("link transfer out-leg: %w", err)
		}
		if err := writeUndoHistory(txDB, userID, out, "created"); err != nil {
			return err
		}
		return writeUndoHistory(txDB, userID, in, "created")
	})
	if err != nil {
		return nil, nil, err
	}

	return out, in, nil
}

// MostRecent returns the user's most recent transaction without modifying
// it. Exported (rather than folded into UndoLast alone) so the correction
// flow below can capture *which* transaction to revert before creating the
// corrected replacement — calling UndoLast after that create would
// otherwise resolve "most recent" to the new transaction itself. Category
// is preloaded so a caller reporting the transaction back (e.g. as a tool
// result) can use the real category name/bucket, not just IDs.
func (s *Service) MostRecent(ctx context.Context, userID uint) (*models.Transaction, error) {
	var tx models.Transaction
	err := s.db.WithContext(ctx).
		Preload("Category").
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

// MostRecentByType returns the user's single most recent transaction of a
// given type (e.g. TxIncome) — backs a budget-allocation question phrased
// against a real amount ("based on my last salary, what's my needs/wants/
// savings split") rather than just the bare configured percentages. Returns
// apperror.ErrNoIncomeTransaction (the only caller today only ever asks for
// TxIncome) when the user has none of that type yet, distinct from
// MostRecent's ErrNoUndoableTransaction since "nothing to undo" and "no
// income recorded yet" are different situations for a caller to explain.
func (s *Service) MostRecentByType(ctx context.Context, userID uint, txType models.TransactionType) (*models.Transaction, error) {
	var tx models.Transaction
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND type = ?", userID, txType).
		Order("occurred_at DESC, created_at DESC").
		First(&tx).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrNoIncomeTransaction
		}
		return nil, fmt.Errorf("find most recent %s transaction: %w", txType, err)
	}
	return &tx, nil
}

// RecentTransactions returns the user's most recent transactions (newest
// first, up to limit), Category preloaded. Backs bot.Service's
// get_recent_transactions tool (see ai.ChainedCorrector) — a
// chained-tool-calling AIClient uses these candidates to identify EXACTLY
// which transaction a correction message refers to, instead of always
// assuming it's the single most recent one. An empty (not nil-error) slice
// means the user has no transactions yet.
func (s *Service) RecentTransactions(ctx context.Context, userID uint, limit int) ([]*models.Transaction, error) {
	var txs []*models.Transaction
	err := s.db.WithContext(ctx).
		Preload("Category").
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Limit(limit).
		Find(&txs).Error
	if err != nil {
		return nil, fmt.Errorf("find recent transactions: %w", err)
	}
	return txs, nil
}

// findOwned returns the user's own transaction with the given ID, scoped by
// user_id so one user can never target (and thus correct/undo) another
// user's transaction even if they somehow guessed a valid ID. Not-found
// (wrong owner or nonexistent) maps to ErrNoUndoableTransaction, the same
// sentinel MostRecent uses for "nothing to correct" — from the caller's
// perspective these are the same situation.
func (s *Service) findOwned(ctx context.Context, userID, txID uint) (*models.Transaction, error) {
	var tx models.Transaction
	err := s.db.WithContext(ctx).
		Preload("Category").
		Where("user_id = ? AND id = ?", userID, txID).
		First(&tx).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrNoUndoableTransaction
		}
		return nil, fmt.Errorf("find transaction %d: %w", txID, err)
	}
	return &tx, nil
}

// UndoLast soft-deletes the most recent transaction for a user and records
// an UndoHistory("undone") snapshot in the same DB transaction. A thin
// wrapper over DeleteTransaction with no target (0) — kept as its own named
// method since /undo is a distinct command, not a natural-language flow.
func (s *Service) UndoLast(ctx context.Context, userID uint) (*models.Transaction, error) {
	return s.DeleteTransaction(ctx, userID, 0)
}

// DeleteTransaction soft-deletes one of the user's existing transactions
// (ai.IntentDelete — "hapus transaksi makan siang tadi") and records an
// UndoHistory("undone") snapshot, the same mechanism /undo and CorrectLast
// both use. When targetTransactionID is set — a chained-tool-calling
// AIClient matched the delete request against a get_recent_transactions
// candidate (see ai.ChainedCorrector) — that exact transaction is targeted,
// scoped to userID so one user can never delete another's transaction.
// Zero falls back to the single most recent transaction, the same
// "unspecified" convention CorrectLast uses for TargetTransactionID.
func (s *Service) DeleteTransaction(ctx context.Context, userID uint, targetTransactionID uint) (*models.Transaction, error) {
	var (
		tx  *models.Transaction
		err error
	)
	if targetTransactionID > 0 {
		tx, err = s.findOwned(ctx, userID, targetTransactionID)
	} else {
		tx, err = s.MostRecent(ctx, userID)
	}
	if err != nil {
		return nil, err
	}
	return s.undoTransaction(ctx, userID, tx)
}

// CorrectLast replaces one of the user's existing transactions with a
// corrected one described by parsed (ai.IntentCorrect — "eh maaf, harusnya
// 1jt deh, typo"): it creates the new transaction first and only reverts
// the previous one on success. This ordering is deliberate — if creation
// fails, the user's original (uncorrected but real) transaction is left
// intact rather than silently deleted with nothing to replace it.
//
// Despite the name (kept for the common case), the transaction reverted is
// NOT always the most recent one: when parsed.TargetTransactionID is set —
// a chained-tool-calling AIClient matched the correction message against a
// get_recent_transactions candidate (see ai.ChainedCorrector) — that exact
// transaction is targeted instead, scoped to userID so one user can never
// correct another's transaction. TargetTransactionID's zero value falls
// back to MostRecent, the original behavior from before targeting existed
// (e.g. voice corrections, or a provider without chained tool calling).
//
// Returns the newly created transaction and, separately, the transaction
// that was reverted (nil if there was nothing to correct — e.g. this was
// the user's very first message, or an unresolvable target ID — which is
// not an error: recording it fresh is still the right outcome). If
// creation itself fails, created is nil and err explains why. If creation
// succeeds but the revert step fails, created is still returned (the
// correction was recorded) alongside a non-nil err describing the revert
// failure, so the caller can decide how to report a partial outcome
// instead of discarding a real transaction.
func (s *Service) CorrectLast(ctx context.Context, userID uint, defaultCurrency string, parsed *ai.ParsedTransaction, source models.TransactionSource, rawInput string) (created *models.Transaction, undone *models.Transaction, err error) {
	var previous *models.Transaction
	if parsed.TargetTransactionID > 0 {
		previous, err = s.findOwned(ctx, userID, parsed.TargetTransactionID)
	} else {
		previous, err = s.MostRecent(ctx, userID)
	}
	if err != nil && !errors.Is(err, apperror.ErrNoUndoableTransaction) {
		return nil, nil, fmt.Errorf("find transaction to correct: %w", err)
	}
	// previous is nil here exactly when err is ErrNoUndoableTransaction —
	// nothing to correct (or the targeted id didn't resolve to this user's
	// own transaction), but we still record the new transaction below.

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
// If tx is one leg of an inter-account transfer (see CreateTransfer),
// LinkedTransactionID identifies its other leg — that leg is reverted in
// the same DB transaction too, so undoing/correcting/deleting either side
// of a transfer always cancels the whole thing atomically, never leaving
// one leg dangling. A linked leg that's already gone (e.g. a concurrent
// double-undo) is tolerated rather than treated as a failure, since the
// goal state — both legs gone — is already satisfied.
func (s *Service) undoTransaction(ctx context.Context, userID uint, tx *models.Transaction) (*models.Transaction, error) {
	err := s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Delete(tx).Error; err != nil {
			return fmt.Errorf("soft-delete transaction: %w", err)
		}
		if err := writeUndoHistory(txDB, userID, tx, "undone"); err != nil {
			return err
		}

		if tx.LinkedTransactionID == nil {
			return nil
		}
		var linked models.Transaction
		err := txDB.Where("user_id = ? AND id = ?", userID, *tx.LinkedTransactionID).First(&linked).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find linked transfer leg: %w", err)
		}
		if err := txDB.Delete(&linked).Error; err != nil {
			return fmt.Errorf("soft-delete linked transfer leg: %w", err)
		}
		return writeUndoHistory(txDB, userID, &linked, "undone")
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
