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

	calculated, err := s.calculatedBalance(ctx, acc.ID)
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

func (s *Service) calculatedBalance(ctx context.Context, accountID uint) (float64, error) {
	type totalRow struct {
		Type  string
		Total float64
	}
	var rows []totalRow
	err := s.db.WithContext(ctx).Model(&models.Transaction{}).
		Select("type, COALESCE(SUM(converted_amount), 0) as total").
		Where("account_id = ?", accountID).
		Group("type").
		Scan(&rows).Error
	if err != nil {
		return 0, err
	}

	var balance float64
	for _, r := range rows {
		balance += balanceSign(models.TransactionType(r.Type)) * r.Total
	}
	return balance, nil
}

// balanceSign determines whether a transaction type increases or decreases
// the user's tracked cash balance.
//
// TODO: once multi-account support exists, "transfer" should net to zero
// across the source/destination accounts instead of always decreasing the
// single tracked balance.
func balanceSign(t models.TransactionType) float64 {
	switch t {
	case models.TxIncome, models.TxReward:
		return 1
	default: // expense, investment, transfer
		return -1
	}
}
