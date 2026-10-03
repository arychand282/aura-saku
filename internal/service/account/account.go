// Package account provides account-resolution and balance-computation
// helpers shared by transaction, reconciliation, and bot.Service. It's
// plain functions rather than a stateful Service, since none of this needs
// anything beyond get-or-create plus a sum query.
package account

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

const defaultAccountName = "Default"

// GetOrCreateDefault returns the user's single default account, creating it
// on first use. Every transaction with no account named at all is
// attributed to this account.
func GetOrCreateDefault(ctx context.Context, db *gorm.DB, userID uint) (*models.Account, error) {
	var acc models.Account
	err := db.WithContext(ctx).
		Where(models.Account{UserID: userID, IsDefault: true}).
		Attrs(models.Account{Name: defaultAccountName}).
		FirstOrCreate(&acc).Error
	if err != nil {
		return nil, fmt.Errorf("get or create default account for user %d: %w", userID, err)
	}
	return &acc, nil
}

// GetOrCreateByName resolves an account by name for this user, creating it
// on first mention — the same auto-create-on-first-use convention
// transaction.resolveCategory already uses for categories. Matching is
// case-insensitive (so "gopay" and "GoPay" never create two accounts), but
// unlike a category's name (internal-only, stored lowercased), an
// account's name IS shown back to the user, so it's stored with whatever
// casing it was first given. An empty/whitespace-only name falls back to
// GetOrCreateDefault, so callers never need their own empty-name branch.
func GetOrCreateByName(ctx context.Context, db *gorm.DB, userID uint, name string) (*models.Account, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return GetOrCreateDefault(ctx, db, userID)
	}

	var acc models.Account
	err := db.WithContext(ctx).
		Where("user_id = ? AND LOWER(name) = LOWER(?)", userID, trimmed).
		Attrs(models.Account{UserID: userID, Name: trimmed}).
		FirstOrCreate(&acc).Error
	if err != nil {
		return nil, fmt.Errorf("get or create account %q for user %d: %w", trimmed, userID, err)
	}
	return &acc, nil
}

// FindByName looks up an existing account by name (same case-insensitive
// matching as GetOrCreateByName) without ever creating one — used by a
// balance query, which must not silently create an account just because it
// was asked about. Returns apperror.ErrAccountNotFound when no such
// account exists for this user.
func FindByName(ctx context.Context, db *gorm.DB, userID uint, name string) (*models.Account, error) {
	trimmed := strings.TrimSpace(name)
	var acc models.Account
	err := db.WithContext(ctx).
		Where("user_id = ? AND LOWER(name) = LOWER(?)", userID, trimmed).
		First(&acc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrAccountNotFound
		}
		return nil, fmt.Errorf("find account %q for user %d: %w", trimmed, userID, err)
	}
	return &acc, nil
}

// SetDefault re-designates which of the user's accounts untagged
// transactions fall back to — resolving targetName via GetOrCreateByName
// (so "jadiin BCA akun default" works even the first time BCA is ever
// mentioned), then atomically unsetting IsDefault on whichever account
// currently holds it and setting it on the target. Only the flag moves:
// the account previously flagged default keeps its own literal name
// ("Default" or whatever it was) and every transaction already posted to
// either account is untouched — AccountID is fixed at creation time, so
// this only changes where a FUTURE unnamed transaction lands (see
// GetOrCreateDefault, which resolves by the IsDefault flag, never by the
// literal name "Default").
func SetDefault(ctx context.Context, db *gorm.DB, userID uint, targetName string) (*models.Account, error) {
	target, err := GetOrCreateByName(ctx, db, userID, targetName)
	if err != nil {
		return nil, err
	}
	if target.IsDefault {
		return target, nil
	}

	err = db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Model(&models.Account{}).
			Where("user_id = ? AND is_default = ?", userID, true).
			Update("is_default", false).Error; err != nil {
			return fmt.Errorf("unset current default account: %w", err)
		}
		if err := txDB.Model(&models.Account{}).
			Where("id = ?", target.ID).
			Update("is_default", true).Error; err != nil {
			return fmt.Errorf("set new default account: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	target.IsDefault = true
	return target, nil
}

// AccountBalance pairs an account with its own calculated balance — backs
// ListWithBalances/the /accounts command.
type AccountBalance struct {
	Account models.Account
	Balance float64
}

// ListWithBalances returns every one of the user's active accounts that
// has at least one active (non-deleted) transaction posted to it (oldest/
// Default first, matching creation order), alongside each one's own
// calculated Balance.
//
// An account with zero active transactions is skipped rather than listed
// at a ₱0 balance — this is what keeps a mis-parsed account (the AI
// mishears a name, or guesses a wrong move_money destination) from
// cluttering /accounts forever: there's no rename/delete command, but once
// the mistaken transaction is undone/corrected away, GetOrCreateByName's
// auto-created account for it naturally drops out of this list on its own,
// with no cleanup step needed. A legitimately-used account that nets to a
// real ₱0 balance (e.g. income and expense canceling out) still has active
// transactions and is unaffected by this filter.
func ListWithBalances(ctx context.Context, db *gorm.DB, userID uint) ([]AccountBalance, error) {
	var accounts []models.Account
	if err := db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at ASC").Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("list accounts for user %d: %w", userID, err)
	}

	result := make([]AccountBalance, 0, len(accounts))
	for _, acc := range accounts {
		var txCount int64
		if err := db.WithContext(ctx).Model(&models.Transaction{}).Where("account_id = ?", acc.ID).Count(&txCount).Error; err != nil {
			return nil, fmt.Errorf("count transactions for account %d: %w", acc.ID, err)
		}
		if txCount == 0 {
			continue
		}

		balance, err := Balance(ctx, db, acc.ID)
		if err != nil {
			return nil, fmt.Errorf("compute balance for account %d: %w", acc.ID, err)
		}
		result = append(result, AccountBalance{Account: acc, Balance: balance})
	}
	return result, nil
}

type totalRow struct {
	Type        string
	TransferLeg *string
	Total       float64
}

// Balance returns the single account's calculated balance: the signed sum
// of every transaction posted to it, sign determined by balanceSign below.
func Balance(ctx context.Context, db *gorm.DB, accountID uint) (float64, error) {
	var rows []totalRow
	err := db.WithContext(ctx).Model(&models.Transaction{}).
		Select("type, transfer_leg, COALESCE(SUM(converted_amount), 0) as total").
		Where("account_id = ?", accountID).
		Group("type, transfer_leg").
		Scan(&rows).Error
	if err != nil {
		return 0, err
	}
	return sumRows(rows), nil
}

// TotalBalance returns the sum of Balance across every one of the user's
// (non-deleted) accounts — their real total net worth, used by the plain
// "what's my balance?" question now that more than one account can exist.
func TotalBalance(ctx context.Context, db *gorm.DB, userID uint) (float64, error) {
	var rows []totalRow
	err := db.WithContext(ctx).Model(&models.Transaction{}).
		Select("transactions.type, transactions.transfer_leg, COALESCE(SUM(transactions.converted_amount), 0) as total").
		Joins("JOIN accounts ON accounts.id = transactions.account_id AND accounts.deleted_at IS NULL").
		Where("accounts.user_id = ?", userID).
		Group("transactions.type, transactions.transfer_leg").
		Scan(&rows).Error
	if err != nil {
		return 0, err
	}
	return sumRows(rows), nil
}

func sumRows(rows []totalRow) float64 {
	var balance float64
	for _, r := range rows {
		var leg *models.TransactionTransferLeg
		if r.TransferLeg != nil {
			l := models.TransactionTransferLeg(*r.TransferLeg)
			leg = &l
		}
		balance += balanceSign(models.TransactionType(r.Type), leg) * r.Total
	}
	return balance
}

// balanceSign determines whether a transaction type/leg increases or
// decreases the account it's posted to. A transfer's sign depends on which
// leg it is (see models.Transaction's own doc comment): an "in" leg
// increases its account, an "out" leg (or a plain external transfer with
// no leg set at all — every row created before multi-account transfers
// existed, and every "sent to another person" transfer today) decreases
// it, exactly like the single-account behavior this replaces.
func balanceSign(t models.TransactionType, leg *models.TransactionTransferLeg) float64 {
	switch t {
	case models.TxIncome, models.TxReward:
		return 1
	case models.TxTransfer:
		if leg != nil && *leg == models.TransferLegIn {
			return 1
		}
		return -1
	default: // expense, investment
		return -1
	}
}
