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

func (s *Service) resolveCategory(ctx context.Context, categoryName string) (*uint, error) {
	name := strings.ToLower(strings.TrimSpace(categoryName))
	if name == "" {
		return nil, nil
	}

	var category models.Category
	err := s.db.WithContext(ctx).
		Where(models.Category{Name: name}).
		Attrs(models.Category{Bucket: models.BucketNone, IsSystem: false}).
		FirstOrCreate(&category).Error
	if err != nil {
		return nil, err
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
