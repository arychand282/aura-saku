package alert_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/alert"
)

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return gormDB
}

func newTestUser(t *testing.T, gormDB *gorm.DB) models.User {
	t.Helper()
	user := models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { cleanup(t, gormDB, user.ID) })
	return user
}

func cleanup(t *testing.T, gormDB *gorm.DB, userID uint) {
	t.Helper()
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.SpendingAlert{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}

// TestCreate_ReplacesExistingActiveAlertForSameCategory covers the
// "replace, don't duplicate" guarantee: setting a second category alert for
// the same category must update the existing row's threshold/period in
// place, never leave two active alerts that would both fire.
func TestCreate_ReplacesExistingActiveAlertForSameCategory(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	first, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeCategory, "food", nil, 1_000_000, "monthly")
	if err != nil {
		t.Fatalf("Create (first): %v", err)
	}

	second, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeCategory, "Food", nil, 2_000_000, "weekly")
	if err != nil {
		t.Fatalf("Create (second, different casing): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("expected the existing alert to be updated in place, got a different id (%d vs %d)", second.ID, first.ID)
	}
	if second.ThresholdAmount != 2_000_000 {
		t.Errorf("ThresholdAmount = %.2f, want 2000000.00 (updated)", second.ThresholdAmount)
	}
	if second.Period != "weekly" {
		t.Errorf("Period = %q, want %q (updated)", second.Period, "weekly")
	}

	var count int64
	gormDB.Model(&models.SpendingAlert{}).Where("user_id = ? AND is_active = ?", user.ID, true).Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 active alert, got %d", count)
	}
}

// TestCreate_DifferentScopesCoexist covers that category/bucket/total
// alerts for the same user are independent and don't collide with each
// other's "replace" logic.
func TestCreate_DifferentScopesCoexist(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	if _, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeCategory, "food", nil, 1_000_000, "monthly"); err != nil {
		t.Fatalf("Create (category): %v", err)
	}
	needs := models.BucketNeeds
	if _, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeBucket, "", &needs, 2_000_000, "monthly"); err != nil {
		t.Fatalf("Create (bucket): %v", err)
	}
	if _, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeTotal, "", nil, 5_000_000, "monthly"); err != nil {
		t.Fatalf("Create (total): %v", err)
	}

	list, err := alert.ListActive(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 independent active alerts, got %d", len(list))
	}
}

// TestCancel_OwnershipScoped covers the guarantee that one user can never
// cancel another's alert, and that canceling removes it from ListActive.
func TestCancel_OwnershipScoped(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)
	otherUser := newTestUser(t, gormDB)

	a, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeCategory, "food", nil, 1_000_000, "monthly")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := alert.Cancel(ctx, gormDB, otherUser.ID, a.ID); !errors.Is(err, apperror.ErrSpendingAlertNotFound) {
		t.Errorf("Cancel by a different user err = %v, want ErrSpendingAlertNotFound", err)
	}

	cancelled, err := alert.Cancel(ctx, gormDB, user.ID, a.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.IsActive {
		t.Error("expected the returned alert to reflect IsActive=false")
	}

	list, err := alert.ListActive(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("ListActive after cancel: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 active alerts after cancel, got %d", len(list))
	}

	if _, err := alert.Cancel(ctx, gormDB, user.ID, a.ID); !errors.Is(err, apperror.ErrSpendingAlertNotFound) {
		t.Errorf("Cancel an already-cancelled alert err = %v, want ErrSpendingAlertNotFound", err)
	}
}
