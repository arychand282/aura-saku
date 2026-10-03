// Package alert provides CRUD for SpendingAlert — a thin layer, mirroring
// internal/service/account's shape, that owns no sum-computation itself:
// computing a scope's current period-to-date spend is entirely
// report.Service's existing job (see bot.Service.currentAlertSpend).
package alert

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

// Create sets up (or updates in place) a standing spending alert. If the
// user already has an ACTIVE alert for the same scope-key — same
// (UserID, Scope, CategoryName) for category, (UserID, Scope, Bucket) for
// bucket, or just (UserID, Scope=total) for total — its ThresholdAmount and
// Period are updated instead of inserting a second alert that would also
// fire, the same "replace, don't duplicate" convention
// savingsgoal.Service.Create already uses for its own single-active-config
// concept.
func Create(ctx context.Context, db *gorm.DB, userID uint, scope models.SpendingAlertScope, categoryName string, bucket *models.BudgetBucket, thresholdAmount float64, period string) (*models.SpendingAlert, error) {
	categoryName = strings.TrimSpace(categoryName)

	query := db.WithContext(ctx).Where("user_id = ? AND scope = ? AND is_active = ?", userID, scope, true)
	switch scope {
	case models.AlertScopeCategory:
		query = query.Where("LOWER(category_name) = LOWER(?)", categoryName)
	case models.AlertScopeBucket:
		query = query.Where("bucket = ?", bucket)
	}

	var existing models.SpendingAlert
	err := query.First(&existing).Error
	switch {
	case err == nil:
		existing.ThresholdAmount = thresholdAmount
		existing.Period = period
		if saveErr := db.WithContext(ctx).Save(&existing).Error; saveErr != nil {
			return nil, fmt.Errorf("update existing spending alert %d: %w", existing.ID, saveErr)
		}
		return &existing, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		alert := &models.SpendingAlert{
			UserID:          userID,
			Scope:           scope,
			CategoryName:    categoryName,
			Bucket:          bucket,
			ThresholdAmount: thresholdAmount,
			Period:          period,
			IsActive:        true,
		}
		if createErr := db.WithContext(ctx).Omit("User").Create(alert).Error; createErr != nil {
			return nil, fmt.Errorf("create spending alert for user %d: %w", userID, createErr)
		}
		return alert, nil
	default:
		return nil, fmt.Errorf("find existing spending alert for user %d: %w", userID, err)
	}
}

// ListActive returns every one of the user's active alerts, oldest first —
// matching account.ListWithBalances's own ordering convention.
func ListActive(ctx context.Context, db *gorm.DB, userID uint) ([]models.SpendingAlert, error) {
	var alerts []models.SpendingAlert
	if err := db.WithContext(ctx).Where("user_id = ? AND is_active = ?", userID, true).Order("created_at ASC").Find(&alerts).Error; err != nil {
		return nil, fmt.Errorf("list active spending alerts for user %d: %w", userID, err)
	}
	return alerts, nil
}

// Cancel deactivates one of the user's own active alerts — scoped to
// userID so one user can never cancel another's, the same ownership
// guarantee transaction.Service.findOwned already provides for
// transactions. Returns apperror.ErrSpendingAlertNotFound when missing or
// not owned by this user.
func Cancel(ctx context.Context, db *gorm.DB, userID, alertID uint) (*models.SpendingAlert, error) {
	var a models.SpendingAlert
	err := db.WithContext(ctx).Where("user_id = ? AND id = ? AND is_active = ?", userID, alertID, true).First(&a).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrSpendingAlertNotFound
		}
		return nil, fmt.Errorf("find spending alert %d for user %d: %w", alertID, userID, err)
	}

	a.IsActive = false
	if err := db.WithContext(ctx).Model(&a).Update("is_active", false).Error; err != nil {
		return nil, fmt.Errorf("deactivate spending alert %d: %w", alertID, err)
	}
	return &a, nil
}
