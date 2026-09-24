// Package account provides the minimal account-resolution helper shared by
// transaction and reconciliation services. It's a plain function rather
// than a stateful Service, since "schema + default account only" doesn't
// yet need anything beyond get-or-create.
package account

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"aura-saku/internal/models"
)

const defaultAccountName = "Default"

// GetOrCreateDefault returns the user's single default account, creating it
// on first use. Every transaction is attributed to this account until
// multi-account bot/dashboard UX exists to create and select others.
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
