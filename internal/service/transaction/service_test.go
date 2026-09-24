package transaction_test

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/transaction"
)

// TestCreateFromParsedAndUndoLast is an integration test against a real
// Postgres instance (see docker-compose.yml). It's skipped automatically if
// no DB is reachable, so `go test ./...` still passes in environments
// without the local dev stack running.
func TestCreateFromParsedAndUndoLast(t *testing.T) {
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

	ctx := context.Background()

	user := models.User{
		TelegramChatID:  time.Now().UnixNano(),
		DefaultCurrency: "IDR",
		Tier:            models.TierFree,
	}
	if err := gormDB.WithContext(ctx).Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { cleanupUser(t, gormDB, user.ID) })

	svc := transaction.NewService(gormDB, fx.NewStaticService())

	parsed := &ai.ParsedTransaction{
		Type:         "expense",
		Amount:       50000,
		Currency:     "IDR",
		CategoryName: "food",
		Description:  "test lunch",
	}

	created, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, parsed, models.SourceText, "lunch 50000")
	if err != nil {
		t.Fatalf("CreateFromParsed: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("expected created transaction to have a non-zero ID")
	}
	if created.CategoryID == nil {
		t.Fatal("expected category to be resolved/created")
	}
	if created.ConvertedAmount != created.OriginalAmount {
		t.Fatalf("expected same-currency conversion to be a no-op, got original=%.2f converted=%.2f", created.OriginalAmount, created.ConvertedAmount)
	}

	var createdHistoryCount int64
	gormDB.Model(&models.UndoHistory{}).Where("transaction_id = ? AND action = ?", created.ID, "created").Count(&createdHistoryCount)
	if createdHistoryCount != 1 {
		t.Fatalf("expected 1 UndoHistory(created) row, got %d", createdHistoryCount)
	}

	undone, err := svc.UndoLast(ctx, user.ID)
	if err != nil {
		t.Fatalf("UndoLast: %v", err)
	}
	if undone.ID != created.ID {
		t.Fatalf("expected UndoLast to revert the just-created transaction %d, got %d", created.ID, undone.ID)
	}

	var afterUndo models.Transaction
	if err := gormDB.Unscoped().First(&afterUndo, created.ID).Error; err != nil {
		t.Fatalf("fetch soft-deleted transaction: %v", err)
	}
	if !afterUndo.DeletedAt.Valid {
		t.Fatal("expected transaction to be soft-deleted after undo")
	}

	var undoneHistoryCount int64
	gormDB.Model(&models.UndoHistory{}).Where("transaction_id = ? AND action = ?", created.ID, "undone").Count(&undoneHistoryCount)
	if undoneHistoryCount != 1 {
		t.Fatalf("expected 1 UndoHistory(undone) row, got %d", undoneHistoryCount)
	}

	if _, err := svc.UndoLast(ctx, user.ID); err == nil {
		t.Fatal("expected UndoLast on a user with no transactions left to return an error")
	}
}

func cleanupUser(t *testing.T, gormDB *gorm.DB, userID uint) {
	t.Helper()
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.UndoHistory{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Transaction{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Account{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}
