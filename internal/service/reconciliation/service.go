package reconciliation

import (
	"context"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

// Service owns the /sync balance-reconciliation flow: comparing a user's
// self-reported actual balance against the app's calculated balance, and
// suggesting a "blind spot" adjustment entry when they diverge. Method
// bodies are TODO-stubbed for this scaffold pass.
type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Sync compares actualBalance against the sum of the user's transactions
// (CalculatedBalance), records a BalanceReconciliation row, and — on a
// discrepancy — should have the AI suggest a blind-spot adjustment
// Transaction.
//
// TODO: compute CalculatedBalance from the sum of ConvertedAmount across the
// user's Transactions (sign per TransactionType), persist a
// BalanceReconciliation row, and when Discrepancy is non-trivial, ask the
// AIClient to phrase a suggested adjustment entry back to the user before
// creating it (only on user confirmation, not automatically).
func (s *Service) Sync(ctx context.Context, userID uint, actualBalance float64) (*models.BalanceReconciliation, error) {
	return nil, apperror.ErrNotImplemented
}
