package report_test

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
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/report"
	"aura-saku/internal/service/transaction"
)

// setupTestDB is an integration test helper against a real Postgres instance
// (see docker-compose.yml) — skipped automatically if no DB is reachable,
// mirroring the pattern used by internal/service/budget and
// internal/service/bot.
func setupTestDB(t *testing.T) *gorm.DB {
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

func newTestUser(t *testing.T, gormDB *gorm.DB) *models.User {
	t.Helper()
	user := &models.User{
		TelegramChatID:  time.Now().UnixNano(),
		DefaultCurrency: "IDR",
		Tier:            models.TierFree,
	}
	if err := gormDB.Create(user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Transaction{})
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Account{})
		gormDB.Unscoped().Where("id = ?", user.ID).Delete(&models.User{})
	})
	return user
}

func TestPeriodRange_InvalidPeriod(t *testing.T) {
	_, _, err := report.PeriodRange("fortnightly")
	if !errors.Is(err, apperror.ErrInvalidPeriod) {
		t.Errorf("err = %v, want apperror.ErrInvalidPeriod", err)
	}
}

func TestSummary(t *testing.T) {
	gormDB := setupTestDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 25_000, Currency: "IDR", CategoryName: "Food",
	}, models.SourceText, "lunch 25000"); err != nil {
		t.Fatalf("create expense: %v", err)
	}
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 100_000, Currency: "IDR", CategoryName: "salary",
	}, models.SourceText, "salary 100000"); err != nil {
		t.Fatalf("create income: %v", err)
	}

	reportSvc := report.NewService(gormDB)
	totals, _, _, err := reportSvc.Summary(ctx, user.ID, "monthly")
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}

	if totals["expense"] != 25_000 {
		t.Errorf("totals[expense] = %.2f, want 25000.00", totals["expense"])
	}
	if totals["income"] != 100_000 {
		t.Errorf("totals[income] = %.2f, want 100000.00", totals["income"])
	}
}

func TestTopCategories(t *testing.T) {
	gormDB := setupTestDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "Food",
	}, models.SourceText, "groceries 300000"); err != nil {
		t.Fatalf("create expense 1: %v", err)
	}
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "Transport",
	}, models.SourceText, "gas 100000"); err != nil {
		t.Fatalf("create expense 2: %v", err)
	}

	reportSvc := report.NewService(gormDB)
	rows, err := reportSvc.TopCategories(ctx, user.ID, "monthly")
	if err != nil {
		t.Fatalf("TopCategories: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("TopCategories returned no rows")
	}
	// transaction.Service.resolveCategory always lowercases on create/lookup
	// (see internal/service/transaction/service.go), so "Food" is stored and
	// returned as "food".
	if rows[0].CategoryName != "food" || rows[0].Total != 300_000 {
		t.Errorf("rows[0] = %+v, want {food 300000}", rows[0])
	}
}

func TestCategoryTotal_Matched(t *testing.T) {
	gormDB := setupTestDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 40_000, Currency: "IDR", CategoryName: "Food",
	}, models.SourceText, "dinner 40000"); err != nil {
		t.Fatalf("create expense: %v", err)
	}

	reportSvc := report.NewService(gormDB)

	// Lowercase query against the stored "Food" category — must match
	// case-insensitively.
	total, matched, err := reportSvc.CategoryTotal(ctx, user.ID, "monthly", "food")
	if err != nil {
		t.Fatalf("CategoryTotal: %v", err)
	}
	if !matched {
		t.Fatalf("matched = false, want true")
	}
	if total != 40_000 {
		t.Errorf("total = %.2f, want 40000.00", total)
	}
}

func TestCategoryTotal_NotFound(t *testing.T) {
	gormDB := setupTestDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	reportSvc := report.NewService(gormDB)
	total, matched, err := reportSvc.CategoryTotal(ctx, user.ID, "monthly", "zzz-not-a-real-category-zzz")
	if err != nil {
		t.Fatalf("CategoryTotal: %v", err)
	}
	if matched {
		t.Errorf("matched = true, want false for a category that doesn't exist")
	}
	if total != 0 {
		t.Errorf("total = %.2f, want 0", total)
	}
}
