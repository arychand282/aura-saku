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

// dateInMonthsAgo returns an OccurredAtHint (YYYY-MM-DD) for the 10th of
// the calendar month monthsAgo months before now — enough to land solidly
// inside that month regardless of today's actual day-of-month.
func dateInMonthsAgo(monthsAgo int) string {
	t := time.Now().UTC().AddDate(0, -monthsAgo, 0)
	return time.Date(t.Year(), t.Month(), 10, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func createExpense(t *testing.T, txSvc *transaction.Service, userID uint, amount float64, category, occurredAtHint string) {
	t.Helper()
	if _, err := txSvc.CreateFromParsed(context.Background(), userID, "IDR", &ai.ParsedTransaction{
		Type: "expense", Amount: amount, Currency: "IDR", CategoryName: category, OccurredAtHint: occurredAtHint,
	}, models.SourceText, category+" "+occurredAtHint); err != nil {
		t.Fatalf("create expense (%s, %s): %v", category, occurredAtHint, err)
	}
}

// TestMonthlyCategoryInsight_TopCategoriesAndIncrease covers the main path:
// last month's top 3 categories by amount, and the single category (out of
// possibly more than 3) that grew the most since the month before, with
// both months' real totals kept (not just the delta) so a caller can show
// a side-by-side comparison.
func TestMonthlyCategoryInsight_TopCategoriesAndIncrease(t *testing.T) {
	gormDB := setupTestDB(t)
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	lastMonth := dateInMonthsAgo(1)
	priorMonth := dateInMonthsAgo(2)

	// Last month: food, transport, entertainment — food and transport are
	// brand new categories (no prior-month baseline), entertainment tripled
	// from 200,000 to 600,000 (+200%, well above the 15% threshold).
	createExpense(t, txSvc, user.ID, 1_200_000, "food", lastMonth)
	createExpense(t, txSvc, user.ID, 800_000, "transport", lastMonth)
	createExpense(t, txSvc, user.ID, 600_000, "entertainment", lastMonth)
	createExpense(t, txSvc, user.ID, 200_000, "entertainment", priorMonth)

	reportSvc := report.NewService(gormDB)
	insight, err := reportSvc.MonthlyCategoryInsight(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("MonthlyCategoryInsight: %v", err)
	}
	if insight == nil {
		t.Fatal("insight = nil, want a real result")
	}

	if insight.TotalExpense != 2_600_000 {
		t.Errorf("TotalExpense = %.2f, want 2600000.00", insight.TotalExpense)
	}
	if len(insight.TopCategories) != 3 {
		t.Fatalf("len(TopCategories) = %d, want 3", len(insight.TopCategories))
	}
	if insight.TopCategories[0].CategoryName != "food" || insight.TopCategories[0].Total != 1_200_000 {
		t.Errorf("TopCategories[0] = %+v, want {food 1200000}", insight.TopCategories[0])
	}

	if insight.IncreasedCategory != "entertainment" {
		t.Fatalf("IncreasedCategory = %q, want \"entertainment\"", insight.IncreasedCategory)
	}
	if insight.IncreasedLastMonth != 600_000 {
		t.Errorf("IncreasedLastMonth = %.2f, want 600000.00", insight.IncreasedLastMonth)
	}
	if insight.IncreasedPriorMonth != 200_000 {
		t.Errorf("IncreasedPriorMonth = %.2f, want 200000.00", insight.IncreasedPriorMonth)
	}
	if insight.IncreasedPercent != 200 {
		t.Errorf("IncreasedPercent = %.2f, want 200.00", insight.IncreasedPercent)
	}
}

// TestMonthlyCategoryInsight_BelowThresholdIsNotFlagged covers the
// significance cutoff: a category that grew, but by less than
// categoryIncreaseThresholdPercent (15%), must not be flagged — otherwise
// ordinary month-to-month noise would get called out every time.
func TestMonthlyCategoryInsight_BelowThresholdIsNotFlagged(t *testing.T) {
	gormDB := setupTestDB(t)
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	lastMonth := dateInMonthsAgo(1)
	priorMonth := dateInMonthsAgo(2)

	// 100,000 -> 110,000 is a 10% increase, below the 15% threshold.
	createExpense(t, txSvc, user.ID, 110_000, "food", lastMonth)
	createExpense(t, txSvc, user.ID, 100_000, "food", priorMonth)

	reportSvc := report.NewService(gormDB)
	insight, err := reportSvc.MonthlyCategoryInsight(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("MonthlyCategoryInsight: %v", err)
	}
	if insight == nil {
		t.Fatal("insight = nil, want a real result (last month did have expenses)")
	}
	if insight.IncreasedCategory != "" {
		t.Errorf("IncreasedCategory = %q, want \"\" (a 10%% increase must not clear the 15%% threshold)", insight.IncreasedCategory)
	}
}

// TestMonthlyCategoryInsight_NoPriorMonthOmitsIncrease covers a brand-new
// user (or a fresh category) with only one month of history: the
// top-categories list still works, but nothing gets flagged as "increased"
// since there's no valid baseline to compare against.
func TestMonthlyCategoryInsight_NoPriorMonthOmitsIncrease(t *testing.T) {
	gormDB := setupTestDB(t)
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	createExpense(t, txSvc, user.ID, 500_000, "food", dateInMonthsAgo(1))

	reportSvc := report.NewService(gormDB)
	insight, err := reportSvc.MonthlyCategoryInsight(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("MonthlyCategoryInsight: %v", err)
	}
	if insight == nil {
		t.Fatal("insight = nil, want a real result")
	}
	if len(insight.TopCategories) != 1 || insight.TopCategories[0].Total != 500_000 {
		t.Errorf("TopCategories = %+v, want a single 500000 food entry", insight.TopCategories)
	}
	if insight.IncreasedCategory != "" {
		t.Errorf("IncreasedCategory = %q, want \"\" (no prior month to compare against)", insight.IncreasedCategory)
	}
}

// TestMonthlyCategoryInsight_NoExpensesReturnsNil covers the "nothing to
// report" case — no expenses at all last month means MonthlyCategoryInsight
// returns (nil, nil), the same "empty means nothing to report" convention
// budget.Service.SuggestRollover uses for an empty string.
func TestMonthlyCategoryInsight_NoExpensesReturnsNil(t *testing.T) {
	gormDB := setupTestDB(t)
	user := newTestUser(t, gormDB)

	reportSvc := report.NewService(gormDB)
	insight, err := reportSvc.MonthlyCategoryInsight(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("MonthlyCategoryInsight: %v", err)
	}
	if insight != nil {
		t.Errorf("insight = %+v, want nil", insight)
	}
}
