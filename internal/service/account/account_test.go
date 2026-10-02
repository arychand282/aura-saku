package account_test

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
	"aura-saku/internal/service/account"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/transaction"
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
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.UndoHistory{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Transaction{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Account{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}

// TestGetOrCreateByName_CaseInsensitiveReuse covers the same
// auto-create-on-first-mention convention transaction.resolveCategory
// already uses for categories: a later mention with different casing must
// reuse the same account, not create a second one, while the originally
// typed casing is preserved for display.
func TestGetOrCreateByName_CaseInsensitiveReuse(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	first, err := account.GetOrCreateByName(ctx, gormDB, user.ID, "Gopay")
	if err != nil {
		t.Fatalf("GetOrCreateByName: %v", err)
	}
	if first.Name != "Gopay" {
		t.Errorf("Name = %q, want original casing %q", first.Name, "Gopay")
	}

	second, err := account.GetOrCreateByName(ctx, gormDB, user.ID, "gopay")
	if err != nil {
		t.Fatalf("GetOrCreateByName (different casing): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("expected case-insensitive reuse, got a different account id (%d vs %d)", second.ID, first.ID)
	}

	var count int64
	gormDB.Model(&models.Account{}).Where("user_id = ? AND name = ?", user.ID, "Gopay").Count(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 Gopay account row, got %d", count)
	}
}

// TestGetOrCreateByName_EmptyFallsBackToDefault covers the convention every
// caller (transaction.Service.CreateFromParsed) relies on: an unnamed
// account reference must land on the same single Default account as before
// this feature existed, with no separate empty-name branch needed at the
// call site.
func TestGetOrCreateByName_EmptyFallsBackToDefault(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	got, err := account.GetOrCreateByName(ctx, gormDB, user.ID, "  ")
	if err != nil {
		t.Fatalf("GetOrCreateByName: %v", err)
	}
	if !got.IsDefault || got.Name != "Default" {
		t.Errorf("got account %+v, want the Default account", got)
	}
}

// TestFindByName_NotFound covers the "never silently create" guarantee a
// balance query relies on: asking about an account that doesn't exist must
// error, not fabricate one.
func TestFindByName_NotFound(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	if _, err := account.FindByName(ctx, gormDB, user.ID, "Nonexistent"); !errors.Is(err, apperror.ErrAccountNotFound) {
		t.Errorf("FindByName err = %v, want ErrAccountNotFound", err)
	}

	var count int64
	gormDB.Model(&models.Account{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Errorf("FindByName must never create an account, found %d", count)
	}
}

// TestTotalBalance_NetsTransferBetweenAccountsToZero is the core
// correctness check for multi-account balance math: moving money between
// two of the user's own accounts must shift each account's individual
// balance while leaving the TOTAL exactly where it was before the move —
// this is what resolves the long-standing
// "transfer should net to zero" TODO this feature implements.
func TestTotalBalance_NetsTransferBetweenAccountsToZero(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 1_000_000, Currency: "IDR", CategoryName: "salary", AccountName: "BCA",
	}, models.SourceText, "gaji 1 juta ke BCA"); err != nil {
		t.Fatalf("create income into BCA: %v", err)
	}

	totalBefore, err := account.TotalBalance(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("TotalBalance (before transfer): %v", err)
	}
	if totalBefore != 1_000_000 {
		t.Fatalf("totalBefore = %.2f, want 1000000.00", totalBefore)
	}

	if _, _, err := txSvc.CreateTransfer(ctx, user.ID, user.DefaultCurrency, "BCA", "Gopay", 300_000, "IDR", "topup", ""); err != nil {
		t.Fatalf("CreateTransfer: %v", err)
	}

	bca, err := account.FindByName(ctx, gormDB, user.ID, "BCA")
	if err != nil {
		t.Fatalf("find BCA: %v", err)
	}
	gopay, err := account.FindByName(ctx, gormDB, user.ID, "Gopay")
	if err != nil {
		t.Fatalf("find Gopay: %v", err)
	}

	bcaBalance, err := account.Balance(ctx, gormDB, bca.ID)
	if err != nil {
		t.Fatalf("Balance(BCA): %v", err)
	}
	if bcaBalance != 700_000 {
		t.Errorf("BCA balance = %.2f, want 700000.00", bcaBalance)
	}

	gopayBalance, err := account.Balance(ctx, gormDB, gopay.ID)
	if err != nil {
		t.Fatalf("Balance(Gopay): %v", err)
	}
	if gopayBalance != 300_000 {
		t.Errorf("Gopay balance = %.2f, want 300000.00", gopayBalance)
	}

	totalAfter, err := account.TotalBalance(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("TotalBalance (after transfer): %v", err)
	}
	if totalAfter != totalBefore {
		t.Errorf("totalAfter = %.2f, want unchanged totalBefore %.2f (transfer must net to zero)", totalAfter, totalBefore)
	}
}

// TestListWithBalances covers the /accounts command's data source: every
// account the user has, each with its own correct balance.
func TestListWithBalances(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 20_000, Currency: "IDR", CategoryName: "food", AccountName: "OVO",
	}, models.SourceText, "beli kopi 20rb pake OVO"); err != nil {
		t.Fatalf("create expense via OVO: %v", err)
	}

	list, err := account.ListWithBalances(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("ListWithBalances: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 account (OVO auto-created, no Default used), got %d", len(list))
	}
	if list[0].Account.Name != "OVO" {
		t.Errorf("Account.Name = %q, want %q", list[0].Account.Name, "OVO")
	}
	if list[0].Balance != -20_000 {
		t.Errorf("Balance = %.2f, want -20000.00", list[0].Balance)
	}
}

// TestListWithBalances_SkipsAccountsWithNoActiveTransactions covers the
// safeguard against a mis-parsed account (the AI mishears a name, or
// guesses a wrong move_money destination) cluttering /accounts forever:
// since there's no rename/delete command, an account that never got a real
// transaction — or whose only transaction was undone/corrected away — must
// not show up at all, rather than appearing at a ₱0 balance with no way to
// clean it up.
func TestListWithBalances_SkipsAccountsWithNoActiveTransactions(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	// Never had any transaction at all (e.g. a stray account created by a
	// misfire that never actually completed).
	if _, err := account.GetOrCreateByName(ctx, gormDB, user.ID, "NeverUsed"); err != nil {
		t.Fatalf("GetOrCreateByName: %v", err)
	}

	// Had a transaction, but it was corrected away — simulating "the AI
	// mis-attributed this to the wrong account and the user fixed it."
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	mistaken, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 20_000, Currency: "IDR", CategoryName: "food", AccountName: "Gopai",
	}, models.SourceText, "beli kopi 20rb pake Gopai")
	if err != nil {
		t.Fatalf("create mistaken transaction: %v", err)
	}
	if _, err := txSvc.DeleteTransaction(ctx, user.ID, mistaken.ID); err != nil {
		t.Fatalf("undo mistaken transaction: %v", err)
	}

	// A real, actively-used account for comparison — must still show up.
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 15_000, Currency: "IDR", CategoryName: "food", AccountName: "Gopay",
	}, models.SourceText, "beli kopi 15rb pake Gopay"); err != nil {
		t.Fatalf("create real transaction: %v", err)
	}

	list, err := account.ListWithBalances(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("ListWithBalances: %v", err)
	}
	if len(list) != 1 {
		names := make([]string, len(list))
		for i, ab := range list {
			names[i] = ab.Account.Name
		}
		t.Fatalf("expected only the actively-used Gopay account, got %d: %v", len(list), names)
	}
	if list[0].Account.Name != "Gopay" {
		t.Errorf("Account.Name = %q, want %q", list[0].Account.Name, "Gopay")
	}

	// The stray accounts still exist in the DB (no deletion), just hidden
	// from the list.
	var strayCount int64
	gormDB.Model(&models.Account{}).Where("user_id = ? AND name IN ?", user.ID, []string{"NeverUsed", "Gopai"}).Count(&strayCount)
	if strayCount != 2 {
		t.Errorf("expected both stray accounts to still exist (just hidden), found %d", strayCount)
	}
}
