package savingsgoal_test

import (
	"context"
	"errors"
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
	"aura-saku/internal/service/savingsgoal"
	"aura-saku/internal/service/transaction"
)

// setup is an integration test helper against a real Postgres instance (see
// docker-compose.yml). Skipped automatically if no DB is reachable, so
// `go test ./...` still passes without the local dev stack.
func setup(t *testing.T) (*savingsgoal.Service, *gorm.DB, *models.User) {
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

	user := &models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.Create(user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.SavingsGoal{})
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Transaction{})
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Account{})
		gormDB.Unscoped().Where("id = ?", user.ID).Delete(&models.User{})
	})

	budgetSvc := budget.NewService(gormDB)
	return savingsgoal.NewService(gormDB, budgetSvc), gormDB, user
}

func TestCreate_RejectsInvalidInput(t *testing.T) {
	svc, _, user := setup(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, user.ID, "laptop", 0, "IDR", nil); !errors.Is(err, apperror.ErrInvalidSavingsGoal) {
		t.Errorf("Create with zero target err = %v, want ErrInvalidSavingsGoal", err)
	}
	if _, err := svc.Create(ctx, user.ID, "  ", 1_000_000, "IDR", nil); !errors.Is(err, apperror.ErrInvalidSavingsGoal) {
		t.Errorf("Create with blank name err = %v, want ErrInvalidSavingsGoal", err)
	}
}

// TestCreate_ReplacesActiveGoalAndDeactivatesOld covers the core contract:
// only one goal is active at a time, so creating a second one deactivates
// (not deletes) the first.
func TestCreate_ReplacesActiveGoalAndDeactivatesOld(t *testing.T) {
	svc, gormDB, user := setup(t)
	ctx := context.Background()

	first, err := svc.Create(ctx, user.ID, "laptop", 10_000_000, "idr", nil)
	if err != nil {
		t.Fatalf("Create (first): %v", err)
	}
	if first.Currency != "IDR" {
		t.Errorf("Currency = %q, want uppercased \"IDR\"", first.Currency)
	}
	if !first.IsActive {
		t.Error("expected the first goal to be active")
	}

	second, err := svc.Create(ctx, user.ID, "emergency fund", 5_000_000, "IDR", nil)
	if err != nil {
		t.Fatalf("Create (second): %v", err)
	}
	if !second.IsActive {
		t.Error("expected the second goal to be active")
	}

	var firstAfter models.SavingsGoal
	if err := gormDB.First(&firstAfter, first.ID).Error; err != nil {
		t.Fatalf("reload first goal: %v", err)
	}
	if firstAfter.IsActive {
		t.Error("expected the first goal to be deactivated, not left active")
	}

	active, err := svc.Active(ctx, user.ID)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != second.ID {
		t.Errorf("Active() returned goal %d, want the second one (%d)", active.ID, second.ID)
	}
}

// TestActive_NoGoalReturnsSentinel covers the "nothing set up yet" case for
// a brand-new user.
func TestActive_NoGoalReturnsSentinel(t *testing.T) {
	svc, _, user := setup(t)
	if _, err := svc.Active(context.Background(), user.ID); !errors.Is(err, apperror.ErrNoActiveSavingsGoal) {
		t.Errorf("Active err = %v, want ErrNoActiveSavingsGoal", err)
	}
}

// TestProgress_TracksRealSavingsActivitySinceGoalCreation covers the actual
// progress computation: only investment-type/savings-bucketed transactions
// count, and only those dated on/after the goal's own creation — reusing
// budget.Service.SavingsSince, the same "what counts as savings" definition
// used everywhere else in the app.
func TestProgress_TracksRealSavingsActivitySinceGoalCreation(t *testing.T) {
	svc, gormDB, user := setup(t)
	ctx := context.Background()
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	goal, err := svc.Create(ctx, user.ID, "laptop", 10_000_000, "IDR", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Counts: an investment-type transaction.
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "investment", Amount: 2_000_000, Currency: "IDR", CategoryName: "mutual fund",
	}, models.SourceText, "invest 2000000"); err != nil {
		t.Fatalf("create investment: %v", err)
	}
	// Counts: an expense explicitly bucketed "savings".
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 1_000_000, Currency: "IDR", CategoryName: "transfer to savings account", Bucket: "savings",
	}, models.SourceText, "transfer to savings 1000000"); err != nil {
		t.Fatalf("create savings-bucketed expense: %v", err)
	}
	// Does NOT count: an ordinary Wants expense.
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 500_000, Currency: "IDR", CategoryName: "entertainment",
	}, models.SourceText, "movie 500000"); err != nil {
		t.Fatalf("create wants expense: %v", err)
	}

	progress, err := svc.Progress(ctx, user.ID)
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if progress.Goal.ID != goal.ID {
		t.Errorf("Progress.Goal.ID = %d, want %d", progress.Goal.ID, goal.ID)
	}
	if progress.Saved != 3_000_000 {
		t.Errorf("Progress.Saved = %.2f, want 3000000.00 (investment + savings-bucketed expense only)", progress.Saved)
	}
}

// TestCancel_DeactivatesAndReturnsSentinelWhenNoneLeft covers cancellation:
// the active goal is deactivated (not deleted), and canceling again (or
// checking progress) correctly reports no active goal afterward.
func TestCancel_DeactivatesAndReturnsSentinelWhenNoneLeft(t *testing.T) {
	svc, gormDB, user := setup(t)
	ctx := context.Background()

	goal, err := svc.Create(ctx, user.ID, "vacation", 3_000_000, "IDR", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	canceled, err := svc.Cancel(ctx, user.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.ID != goal.ID {
		t.Errorf("Cancel returned goal %d, want %d", canceled.ID, goal.ID)
	}

	var reloaded models.SavingsGoal
	if err := gormDB.First(&reloaded, goal.ID).Error; err != nil {
		t.Fatalf("reload canceled goal: %v", err)
	}
	if reloaded.IsActive {
		t.Error("expected the canceled goal to be deactivated, not left active (and not deleted)")
	}

	if _, err := svc.Active(ctx, user.ID); !errors.Is(err, apperror.ErrNoActiveSavingsGoal) {
		t.Errorf("Active after cancel err = %v, want ErrNoActiveSavingsGoal", err)
	}
	if _, err := svc.Cancel(ctx, user.ID); !errors.Is(err, apperror.ErrNoActiveSavingsGoal) {
		t.Errorf("Cancel with nothing left err = %v, want ErrNoActiveSavingsGoal", err)
	}
}
