package transaction

import (
	"context"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
	"aura-saku/internal/service/fx"
)

// Service owns persistence of transactions, including multi-currency
// conversion and the /undo flow. Method bodies are TODO-stubbed for this
// scaffold pass — signatures are fixed so callers (bot.Service) and the
// dashboard API can be wired against a stable contract now.
type Service struct {
	db    *gorm.DB
	fxSvc fx.Service
}

func NewService(db *gorm.DB, fxSvc fx.Service) *Service {
	return &Service{db: db, fxSvc: fxSvc}
}

// CreateFromParsed persists a Transaction from an AI-parsed result.
//
// TODO: resolve-or-create the Category from parsed.CategoryName, resolve
// parsed.OccurredAtHint into a concrete OccurredAt timestamp, fetch the
// historical exchange rate via s.fxSvc.GetExchangeRate, compute
// ConvertedAmount, persist the row, and fire an AIUsageLog write via a
// non-blocking goroutine so the caller's reply isn't delayed by the log insert.
func (s *Service) CreateFromParsed(ctx context.Context, userID uint, defaultCurrency string, parsed *ai.ParsedTransaction, source models.TransactionSource, rawInput string) (*models.Transaction, error) {
	return nil, apperror.ErrNotImplemented
}

// UndoLast reverts the most recent transaction for a user by soft-deleting
// it and recording an UndoHistory snapshot.
//
// TODO: query the latest non-deleted Transaction for userID ordered by
// CreatedAt desc, soft-delete it (GORM's default Delete respects
// DeletedAt), write an UndoHistory row with Action="undone" and a
// SnapshotJSON of the pre-delete row, and return the undone transaction.
func (s *Service) UndoLast(ctx context.Context, userID uint) (*models.Transaction, error) {
	return nil, apperror.ErrNotImplemented
}
