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

	rate, err := s.fxSvc.GetExchangeRate(ctx, parsed.Currency, defaultCurrency)
	if err != nil {
		return nil, fmt.Errorf("resolve exchange rate: %w", err)
	}

	tx := &models.Transaction{
		UserID:           userID,
		CategoryID:       categoryID,
		Type:             models.TransactionType(parsed.Type),
		Description:      parsed.Description,
		OriginalAmount:   parsed.Amount,
		OriginalCurrency: strings.ToUpper(parsed.Currency),
		ExchangeRate:     rate,
		ConvertedAmount:  parsed.Amount * rate,
		Source:           source,
		RawInput:         rawInput,
		OccurredAt:       resolveOccurredAt(parsed.OccurredAtHint),
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
	txType := models.TxIncome
	absAmount := amount
	if amount < 0 {
		txType = models.TxExpense
		absAmount = -amount
	}

	tx := &models.Transaction{
		UserID:           userID,
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

	err := s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
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

// UndoLast soft-deletes the most recent transaction for a user and records
// an UndoHistory("undone") snapshot in the same DB transaction.
func (s *Service) UndoLast(ctx context.Context, userID uint) (*models.Transaction, error) {
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

	err = s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Delete(&tx).Error; err != nil {
			return fmt.Errorf("soft-delete transaction: %w", err)
		}
		return writeUndoHistory(txDB, userID, &tx, "undone")
	})
	if err != nil {
		return nil, err
	}

	return &tx, nil
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

// resolveOccurredAt interprets the AI's relative-time hint into a concrete
// timestamp.
//
// TODO: handle richer relative hints ("3 days ago", weekday names, explicit
// dates) — anything unrecognized currently defaults to now.
func resolveOccurredAt(hint string) time.Time {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(hint)) {
	case "", "today", "now":
		return now
	case "yesterday":
		return now.AddDate(0, 0, -1)
	default:
		return now
	}
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
