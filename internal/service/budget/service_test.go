package budget_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/transaction"
)

// TestEvaluateBudget is an integration test against a real Postgres instance
// (see docker-compose.yml). It's skipped automatically if no DB is
// reachable, so `go test ./...` still passes without the local dev stack.
func TestEvaluateBudget(t *testing.T) {
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
	t.Cleanup(func() { cleanup(t, gormDB, user.ID) })

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	budgetSvc := budget.NewService(gormDB)

	// Income: 5,000,000 IDR this month -> Needs limit 2,500,000 (50%).
	_, err = txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 5_000_000, Currency: "IDR", CategoryName: "salary",
	}, models.SourceText, "salary 5000000")
	if err != nil {
		t.Fatalf("create income transaction: %v", err)
	}

	// Needs expense of 3,000,000 exceeds the 2,500,000 Needs limit.
	_, err = txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 3_000_000, Currency: "IDR", CategoryName: "food",
	}, models.SourceText, "food 3000000")
	if err != nil {
		t.Fatalf("create needs expense: %v", err)
	}

	// Wants expense of 100,000 stays well under the 1,500,000 (30%) limit.
	_, err = txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "entertainment",
	}, models.SourceText, "movie 100000")
	if err != nil {
		t.Fatalf("create wants expense: %v", err)
	}

	status, err := budgetSvc.EvaluateBudget(ctx, user.ID, models.LanguageEN)
	if err != nil {
		t.Fatalf("EvaluateBudget: %v", err)
	}

	if status.NeedsLimit != 2_500_000 {
		t.Fatalf("expected NeedsLimit 2,500,000 (50%% of income), got %.2f", status.NeedsLimit)
	}
	if status.NeedsSpent != 3_000_000 {
		t.Fatalf("expected NeedsSpent 3,000,000, got %.2f", status.NeedsSpent)
	}
	if status.WantsLimit != 1_500_000 {
		t.Fatalf("expected WantsLimit 1,500,000 (30%% of income), got %.2f", status.WantsLimit)
	}
	if status.WantsSpent != 100_000 {
		t.Fatalf("expected WantsSpent 100,000, got %.2f", status.WantsSpent)
	}

	foundNeedsWarning := false
	for _, w := range status.Warnings {
		if strings.Contains(w, "Needs spending has exceeded") {
			foundNeedsWarning = true
		}
	}
	if !foundNeedsWarning {
		t.Fatalf("expected a Needs-exceeded warning, got: %v", status.Warnings)
	}
}

func cleanup(t *testing.T, gormDB *gorm.DB, userID uint) {
	t.Helper()
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.UndoHistory{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Transaction{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Budget{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.BalanceReconciliation{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Account{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}
