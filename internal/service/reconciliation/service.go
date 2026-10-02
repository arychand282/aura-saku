package reconciliation

import (
	"context"
	"fmt"
	"math"

	"gorm.io/gorm"

	"aura-saku/internal/models"
	"aura-saku/internal/service/account"
	"aura-saku/internal/service/transaction"
)

// discrepancyTolerance is the smallest difference treated as "matched"
// rather than a real discrepancy, absorbing float rounding noise.
const discrepancyTolerance = 0.01

// Service owns the /sync balance-reconciliation flow: comparing a user's
// self-reported actual balance against the app's calculated balance, and
// recording a "blind spot" adjustment entry when they diverge.
type Service struct {
	db    *gorm.DB
	txSvc *transaction.Service
}

func NewService(db *gorm.DB, txSvc *transaction.Service) *Service {
	return &Service{db: db, txSvc: txSvc}
}

// Sync compares actualBalance against the calculated balance from the
// user's transaction history. On a match, it just records the check. On a
// discrepancy, it immediately records a blind-spot adjustment Transaction
// (the same "commit immediately, /undo to revert" pattern used elsewhere in
// this bot, rather than a separate interactive confirmation step) so future
// syncs reconcile cleanly.
func (s *Service) Sync(ctx context.Context, userID uint, actualBalance float64) (*models.BalanceReconciliation, error) {
	// /sync has no account-selection UX yet, so it always targets the
	// user's single default account (see internal/service/account).
	acc, err := account.GetOrCreateDefault(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}

	calculated, err := account.Balance(ctx, s.db, acc.ID)
	if err != nil {
		return nil, fmt.Errorf("compute calculated balance: %w", err)
	}

	discrepancy := actualBalance - calculated
	rec := &models.BalanceReconciliation{
		UserID:            userID,
		AccountID:         &acc.ID,
		ActualBalance:     actualBalance,
		CalculatedBalance: calculated,
		Discrepancy:       discrepancy,
		Status:            models.ReconMatched,
	}

	if math.Abs(discrepancy) <= discrepancyTolerance {
		if err := s.db.WithContext(ctx).Create(rec).Error; err != nil {
			return nil, fmt.Errorf("insert reconciliation record: %w", err)
		}
		return rec, nil
	}

	defaultCurrency := "IDR"
	var user models.User
	if err := s.db.WithContext(ctx).Select("default_currency").First(&user, userID).Error; err == nil && user.DefaultCurrency != "" {
		defaultCurrency = user.DefaultCurrency
	}

	adjustmentTx, err := s.txSvc.CreateAdjustment(ctx, userID, discrepancy, defaultCurrency, "Balance reconciliation adjustment (blind spot)")
	if err != nil {
		return nil, fmt.Errorf("create adjustment transaction: %w", err)
	}

	rec.Status = models.ReconAdjusted
	rec.SuggestedAdjustmentTxID = &adjustmentTx.ID

	if err := s.db.WithContext(ctx).Create(rec).Error; err != nil {
		return nil, fmt.Errorf("insert reconciliation record: %w", err)
	}

	return rec, nil
}

// Balance returns the user's current calculated balance with no side
// effects — no BalanceReconciliation row, no adjustment transaction, just
// the number. Used by the bot's /sync-with-no-argument and "what's my
// balance" natural-language paths, as distinct from Sync's full
// reconcile-against-a-self-reported-actual-balance flow above. This now
// means the user's total across every one of their accounts (their real
// net worth), not just the single default account — see
// account.TotalBalance, which is also where the sign logic for an
// inter-account transfer's two legs netting to zero lives.
func (s *Service) Balance(ctx context.Context, userID uint) (float64, error) {
	return account.TotalBalance(ctx, s.db, userID)
}
