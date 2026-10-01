package transaction_test

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

// TestCreateFromParsed_BucketOverride covers the AI's per-transaction
// bucket classification taking precedence over the category's own static
// default: "shopping" defaults to Wants (see defaultCategoryBuckets), but
// an AI-classified "needs" override (e.g. an urgent replacement purchase)
// must be persisted and honored, not silently discarded in favor of the
// category default.
func TestCreateFromParsed_BucketOverride(t *testing.T) {
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

	created, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 480_000, Currency: "IDR", CategoryName: "shopping", Bucket: "needs",
	}, models.SourceText, "new shoes, old ones broke, 480rb")
	if err != nil {
		t.Fatalf("CreateFromParsed: %v", err)
	}

	if created.Bucket == nil {
		t.Fatal("expected Bucket override to be persisted, got nil")
	}
	if *created.Bucket != models.BucketNeeds {
		t.Errorf("Bucket = %q, want %q (the AI override, not shopping's own wants default)", *created.Bucket, models.BucketNeeds)
	}

	var category models.Category
	if err := gormDB.First(&category, *created.CategoryID).Error; err != nil {
		t.Fatalf("load category: %v", err)
	}
	if category.Bucket != models.BucketWants {
		t.Fatalf("expected shopping's own default bucket to remain wants (unaffected by the transaction-level override), got %q", category.Bucket)
	}
}

// TestCreateFromParsed_UnrecognizedBucketIsNoOverride covers the fallback
// path: an empty or unrecognized Bucket from the AI must not fail the parse
// or get force-cast into a bogus value — it's simply "no override".
func TestCreateFromParsed_UnrecognizedBucketIsNoOverride(t *testing.T) {
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

	created, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 25_000, Currency: "IDR", CategoryName: "food", Bucket: "",
	}, models.SourceText, "lunch 25000")
	if err != nil {
		t.Fatalf("CreateFromParsed: %v", err)
	}
	if created.Bucket != nil {
		t.Errorf("Bucket = %v, want nil for an empty AI classification", *created.Bucket)
	}
}

// TestDeleteTransaction_TargetsSpecificTransactionNotJustLatest covers
// natural-language deletion of a specific transaction from among several,
// the same targeting mechanism as CorrectLast's TargetTransactionID (see
// internal/service/bot's handleTransactionDeletion and
// ai.ChainedCorrector's get_recent_transactions): passing the fuel
// transaction's own ID must delete exactly that one, leaving the more
// recent lunch and dinner transactions untouched — not silently fall back
// to "most recent" the way DeleteTransaction(ctx, userID, 0) would.
func TestDeleteTransaction_TargetsSpecificTransactionNotJustLatest(t *testing.T) {
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

	fuel, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "transport", Description: "bensin",
	}, models.SourceText, "bensin 300k")
	if err != nil {
		t.Fatalf("CreateFromParsed(fuel): %v", err)
	}
	lunch, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 300_000, Currency: "IDR", CategoryName: "food", Description: "makan siang",
	}, models.SourceText, "makan siang 300k")
	if err != nil {
		t.Fatalf("CreateFromParsed(lunch): %v", err)
	}
	dinner, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 100_000, Currency: "IDR", CategoryName: "food", Description: "makan malam",
	}, models.SourceText, "makan malam 100k")
	if err != nil {
		t.Fatalf("CreateFromParsed(dinner): %v", err)
	}

	deleted, err := svc.DeleteTransaction(ctx, user.ID, fuel.ID)
	if err != nil {
		t.Fatalf("DeleteTransaction: %v", err)
	}
	if deleted.ID != fuel.ID {
		t.Fatalf("DeleteTransaction deleted transaction %d, want the targeted fuel transaction %d", deleted.ID, fuel.ID)
	}

	var fuelAfter models.Transaction
	if err := gormDB.Unscoped().First(&fuelAfter, fuel.ID).Error; err != nil {
		t.Fatalf("fetch fuel transaction: %v", err)
	}
	if !fuelAfter.DeletedAt.Valid {
		t.Error("targeted fuel transaction should be soft-deleted, was not")
	}

	for _, untouched := range []*models.Transaction{lunch, dinner} {
		var got models.Transaction
		if err := gormDB.First(&got, untouched.ID).Error; err != nil {
			t.Errorf("transaction %d (%s) should still exist untouched: %v", untouched.ID, untouched.Description, err)
		}
	}

	// A target ID belonging to a different user must never resolve — same
	// ownership guarantee CorrectLast relies on.
	otherUser := models.User{
		TelegramChatID:  time.Now().UnixNano() + 1,
		DefaultCurrency: "IDR",
		Tier:            models.TierFree,
	}
	if err := gormDB.WithContext(ctx).Create(&otherUser).Error; err != nil {
		t.Fatalf("create second test user: %v", err)
	}
	t.Cleanup(func() { cleanupUser(t, gormDB, otherUser.ID) })

	if _, err := svc.DeleteTransaction(ctx, otherUser.ID, lunch.ID); !errors.Is(err, apperror.ErrNoUndoableTransaction) {
		t.Errorf("DeleteTransaction across users err = %v, want ErrNoUndoableTransaction (must not cross ownership)", err)
	}
	var lunchStillActive models.Transaction
	if err := gormDB.First(&lunchStillActive, lunch.ID).Error; err != nil {
		t.Errorf("lunch transaction should still be active after a cross-user delete attempt: %v", err)
	}
}

// TestMostRecentByType covers the lookup backing a budget-allocation
// question asked against the user's real last-recorded income
// ("berdasarkan gaji terakhir, budgetku berapa?"): it must return the newest
// transaction of exactly the requested type, never a newer expense that
// happens to be more recent, and apperror.ErrNoIncomeTransaction when the
// user has none of that type yet.
func TestMostRecentByType(t *testing.T) {
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
	t.Cleanup(func() { cleanupUser(t, gormDB, user.ID) })

	svc := transaction.NewService(gormDB, fx.NewStaticService())

	if _, err := svc.MostRecentByType(ctx, user.ID, models.TxIncome); !errors.Is(err, apperror.ErrNoIncomeTransaction) {
		t.Errorf("MostRecentByType with no income recorded err = %v, want ErrNoIncomeTransaction", err)
	}

	salary, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 5_000_000, Currency: "IDR", CategoryName: "salary", Description: "gaji",
	}, models.SourceText, "gaji 5 juta")
	if err != nil {
		t.Fatalf("create salary: %v", err)
	}

	// A newer EXPENSE must never shadow the salary when asking for income
	// specifically.
	if _, err := svc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 25_000, Currency: "IDR", CategoryName: "food",
	}, models.SourceText, "lunch 25000"); err != nil {
		t.Fatalf("create expense: %v", err)
	}

	got, err := svc.MostRecentByType(ctx, user.ID, models.TxIncome)
	if err != nil {
		t.Fatalf("MostRecentByType: %v", err)
	}
	if got.ID != salary.ID {
		t.Errorf("MostRecentByType returned transaction %d, want the salary one (%d)", got.ID, salary.ID)
	}
	if got.ConvertedAmount != 5_000_000 {
		t.Errorf("ConvertedAmount = %.2f, want 5000000.00", got.ConvertedAmount)
	}
}

func cleanupUser(t *testing.T, gormDB *gorm.DB, userID uint) {
	t.Helper()
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.UndoHistory{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Transaction{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Account{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}
