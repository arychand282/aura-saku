// Package debt provides debt-resolution and remaining-amount computation
// helpers shared by transaction.Service and bot.Service — the
// counterparty-tracking analog of internal/service/account. Plain
// functions rather than a stateful Service, same reasoning as account: none
// of this needs anything beyond get-or-create plus a sum query.
package debt

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

// GetOrCreateByCounterparty resolves a debt by (counterparty, direction)
// for this user, creating it on first mention of a new loan — the same
// auto-create-on-first-use convention account.GetOrCreateByName uses.
// Matching is case-insensitive but the original casing is stored, since a
// counterparty's name is shown back to the user.
func GetOrCreateByCounterparty(ctx context.Context, db *gorm.DB, userID uint, counterparty string, direction models.DebtDirection) (*models.Debt, error) {
	trimmed := strings.TrimSpace(counterparty)
	var d models.Debt
	err := db.WithContext(ctx).
		Where("user_id = ? AND LOWER(counterparty) = LOWER(?) AND direction = ?", userID, trimmed, direction).
		Attrs(models.Debt{UserID: userID, Counterparty: trimmed, Direction: direction}).
		FirstOrCreate(&d).Error
	if err != nil {
		return nil, fmt.Errorf("get or create debt with %q (%s) for user %d: %w", trimmed, direction, userID, err)
	}
	return &d, nil
}

// FindByCounterparty looks up an existing debt by (counterparty, direction)
// without ever creating one — a repayment must match a real existing loan,
// never silently create a debt from nothing the way a tagged expense
// auto-creates an account. Returns apperror.ErrDebtNotFound when none
// exists for this user.
func FindByCounterparty(ctx context.Context, db *gorm.DB, userID uint, counterparty string, direction models.DebtDirection) (*models.Debt, error) {
	trimmed := strings.TrimSpace(counterparty)
	var d models.Debt
	err := db.WithContext(ctx).
		Where("user_id = ? AND LOWER(counterparty) = LOWER(?) AND direction = ?", userID, trimmed, direction).
		First(&d).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrDebtNotFound
		}
		return nil, fmt.Errorf("find debt with %q (%s) for user %d: %w", trimmed, direction, userID, err)
	}
	return &d, nil
}

// RemainingAmount returns how much is still outstanding on this debt: the
// signed sum of every linked transaction's ConvertedAmount — a loan
// transaction (TxLoanOut/TxLoanIn) adds to what's outstanding, a repayment
// (TxRepaymentIn/TxRepaymentOut) subtracts — always expressed in the user's
// default currency (ConvertedAmount's own convention), exactly like
// account.Balance. Computed live rather than stored, so undoing/correcting
// any linked transaction automatically keeps this correct with no
// special-case code.
func RemainingAmount(ctx context.Context, db *gorm.DB, debtID uint) (float64, error) {
	type totalRow struct {
		Type  string
		Total float64
	}
	var rows []totalRow
	err := db.WithContext(ctx).Model(&models.Transaction{}).
		Select("type, COALESCE(SUM(converted_amount), 0) as total").
		Where("debt_id = ?", debtID).
		Group("type").
		Scan(&rows).Error
	if err != nil {
		return 0, err
	}

	var remaining float64
	for _, r := range rows {
		switch models.TransactionType(r.Type) {
		case models.TxLoanOut, models.TxLoanIn:
			remaining += r.Total
		case models.TxRepaymentIn, models.TxRepaymentOut:
			remaining -= r.Total
		}
	}
	return remaining, nil
}

// DebtWithRemaining pairs a debt with its own computed RemainingAmount —
// backs ListOpen/the /debts command.
type DebtWithRemaining struct {
	Debt      models.Debt
	Remaining float64
}

// ListOpen returns every one of the user's debts whose RemainingAmount is
// non-zero (oldest first, matching creation order) — a fully repaid debt
// (remaining == 0) is skipped rather than listed at ₱0, the same
// "computed-away, not deleted" pattern account.ListWithBalances uses for an
// empty account: the row stays in the DB (undo-able, auditable) but simply
// stops showing up once nothing outstanding points at it.
func ListOpen(ctx context.Context, db *gorm.DB, userID uint) ([]DebtWithRemaining, error) {
	var debts []models.Debt
	if err := db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC").Find(&debts).Error; err != nil {
		return nil, fmt.Errorf("list debts for user %d: %w", userID, err)
	}

	result := make([]DebtWithRemaining, 0, len(debts))
	for _, d := range debts {
		remaining, err := RemainingAmount(ctx, db, d.ID)
		if err != nil {
			return nil, fmt.Errorf("compute remaining amount for debt %d: %w", d.ID, err)
		}
		if remaining == 0 {
			continue
		}
		result = append(result, DebtWithRemaining{Debt: d, Remaining: remaining})
	}
	return result, nil
}
