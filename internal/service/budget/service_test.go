package budget_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
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

// TestEvaluateBudget_TransactionBucketOverridesCategory covers a
// transaction-level Bucket override (set from the AI's per-message
// classification) taking precedence over its category's static default
// when computing Needs/Wants spend: two expenses in the same
// Wants-default "shopping" category, one with a Needs override, must split
// across NeedsSpent/WantsSpent accordingly rather than both landing in
// Wants.
func TestEvaluateBudget_TransactionBucketOverridesCategory(t *testing.T) {
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

	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 5_000_000, Currency: "IDR", CategoryName: "salary",
	}, models.SourceText, "salary 5000000"); err != nil {
		t.Fatalf("create income: %v", err)
	}

	// No override: "shopping" defaults to Wants.
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "shopping",
	}, models.SourceText, "new shirt 100000"); err != nil {
		t.Fatalf("create wants-default expense: %v", err)
	}

	// Same category, but the AI classified this specific purchase as an
	// urgent/necessary replacement -> Needs override.
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 480_000, Currency: "IDR", CategoryName: "shopping", Bucket: "needs",
	}, models.SourceText, "new shoes, old ones broke, 480rb"); err != nil {
		t.Fatalf("create needs-override expense: %v", err)
	}

	status, err := budgetSvc.EvaluateBudget(ctx, user.ID, models.LanguageEN)
	if err != nil {
		t.Fatalf("EvaluateBudget: %v", err)
	}

	if status.WantsSpent != 100_000 {
		t.Errorf("WantsSpent = %.2f, want 100000.00 (only the non-overridden shopping expense)", status.WantsSpent)
	}
	if status.NeedsSpent != 480_000 {
		t.Errorf("NeedsSpent = %.2f, want 480000.00 (the Needs-overridden shopping expense)", status.NeedsSpent)
	}
}

// TestCurrentBudget_DefaultsTo503020 covers the auto-creation path: a user
// with no budget row yet gets the standard 50/30/20 split, matching
// EvaluateBudget's own getOrCreateActiveBudget fallback.
func TestCurrentBudget_DefaultsTo503020(t *testing.T) {
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
	user := models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.WithContext(ctx).Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { cleanup(t, gormDB, user.ID) })

	budgetSvc := budget.NewService(gormDB)
	b, err := budgetSvc.CurrentBudget(ctx, user.ID)
	if err != nil {
		t.Fatalf("CurrentBudget: %v", err)
	}
	if b.NeedsPercent != 50 || b.WantsPercent != 30 || b.SavingsPercent != 20 {
		t.Errorf("CurrentBudget = %+v, want the default 50/30/20 split", b)
	}
	if !b.IsActive {
		t.Error("expected the auto-created budget to be active")
	}
}

// TestSetBudget_ReplacesActiveAllocationAndDeactivatesOld covers the core
// contract: setting a new allocation deactivates the previous active
// budget (preserving history, not deleting it) rather than mutating it in
// place, and EvaluateBudget immediately picks up the new percentages.
func TestSetBudget_ReplacesActiveAllocationAndDeactivatesOld(t *testing.T) {
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
	user := models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.WithContext(ctx).Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { cleanup(t, gormDB, user.ID) })

	budgetSvc := budget.NewService(gormDB)
	original, err := budgetSvc.CurrentBudget(ctx, user.ID)
	if err != nil {
		t.Fatalf("CurrentBudget (seed default): %v", err)
	}

	updated, err := budgetSvc.SetBudget(ctx, user.ID, 60, 20, 20)
	if err != nil {
		t.Fatalf("SetBudget: %v", err)
	}
	if updated.NeedsPercent != 60 || updated.WantsPercent != 20 || updated.SavingsPercent != 20 {
		t.Errorf("SetBudget result = %+v, want {60 20 20}", updated)
	}
	if !updated.IsActive {
		t.Error("expected the new budget to be active")
	}

	var originalAfter models.Budget
	if err := gormDB.First(&originalAfter, original.ID).Error; err != nil {
		t.Fatalf("reload original budget: %v", err)
	}
	if originalAfter.IsActive {
		t.Error("expected the previous budget to be deactivated, not left active")
	}

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 1_000_000, Currency: "IDR", CategoryName: "salary",
	}, models.SourceText, "salary 1000000"); err != nil {
		t.Fatalf("create income: %v", err)
	}

	status, err := budgetSvc.EvaluateBudget(ctx, user.ID, models.LanguageEN)
	if err != nil {
		t.Fatalf("EvaluateBudget: %v", err)
	}
	if status.NeedsLimit != 600_000 {
		t.Errorf("NeedsLimit = %.2f, want 600000.00 (60%% of 1,000,000 under the new allocation)", status.NeedsLimit)
	}
}

// TestSetBudget_RejectsInvalidPercentages covers validation: a negative
// percentage and a set that doesn't sum to 100 must both be rejected with
// ErrInvalidBudgetPercentages, and must never write a new active budget row.
func TestSetBudget_RejectsInvalidPercentages(t *testing.T) {
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
	user := models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.WithContext(ctx).Create(&user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() { cleanup(t, gormDB, user.ID) })

	budgetSvc := budget.NewService(gormDB)

	if _, err := budgetSvc.SetBudget(ctx, user.ID, 50, 30, 30); !errors.Is(err, apperror.ErrInvalidBudgetPercentages) {
		t.Errorf("SetBudget(50,30,30) err = %v, want ErrInvalidBudgetPercentages (sums to 110)", err)
	}
	if _, err := budgetSvc.SetBudget(ctx, user.ID, -10, 60, 50); !errors.Is(err, apperror.ErrInvalidBudgetPercentages) {
		t.Errorf("SetBudget(-10,60,50) err = %v, want ErrInvalidBudgetPercentages (negative)", err)
	}

	var count int64
	gormDB.Model(&models.Budget{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Errorf("budget row count after only-invalid SetBudget calls = %d, want 0", count)
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
