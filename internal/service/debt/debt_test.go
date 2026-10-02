package debt_test

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
	"aura-saku/internal/service/debt"
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
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Debt{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Account{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}

// TestGetOrCreateByCounterparty_CaseInsensitiveReuse mirrors
// account.GetOrCreateByName's own equivalent test: a later mention with
// different casing of the same person's name, same direction, must reuse
// the same debt rather than creating a second one.
func TestGetOrCreateByCounterparty_CaseInsensitiveReuse(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	first, err := debt.GetOrCreateByCounterparty(ctx, gormDB, user.ID, "Budi", models.DebtOwedToMe)
	if err != nil {
		t.Fatalf("GetOrCreateByCounterparty: %v", err)
	}
	second, err := debt.GetOrCreateByCounterparty(ctx, gormDB, user.ID, "budi", models.DebtOwedToMe)
	if err != nil {
		t.Fatalf("GetOrCreateByCounterparty (different casing): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("expected case-insensitive reuse, got a different debt id (%d vs %d)", second.ID, first.ID)
	}

	// A different DIRECTION with the same name must be a SEPARATE debt —
	// lending to Budi and owing Budi are independent IOUs.
	third, err := debt.GetOrCreateByCounterparty(ctx, gormDB, user.ID, "Budi", models.DebtIOwe)
	if err != nil {
		t.Fatalf("GetOrCreateByCounterparty (other direction): %v", err)
	}
	if third.ID == first.ID {
		t.Error("expected a different debt for the opposite direction with the same person")
	}
}

// TestFindByCounterparty_NotFound covers the guarantee a repayment relies
// on: it must never silently fabricate a debt that was never lent/borrowed.
func TestFindByCounterparty_NotFound(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)

	if _, err := debt.FindByCounterparty(ctx, gormDB, user.ID, "Nobody", models.DebtOwedToMe); !errors.Is(err, apperror.ErrDebtNotFound) {
		t.Errorf("FindByCounterparty err = %v, want ErrDebtNotFound", err)
	}
	var count int64
	gormDB.Model(&models.Debt{}).Where("user_id = ?", user.ID).Count(&count)
	if count != 0 {
		t.Errorf("FindByCounterparty must never create a debt, found %d", count)
	}
}

// TestRemainingAmount_NetsLoanMinusRepayments is the core correctness
// check: lending money creates an outstanding amount, a partial repayment
// reduces it, and a full repayment brings it to exactly zero.
func TestRemainingAmount_NetsLoanMinusRepayments(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	_, debtRow, err := txSvc.CreateLoan(ctx, user.ID, user.DefaultCurrency, "Budi", models.DebtOwedToMe, 500_000, "IDR", "", "", "")
	if err != nil {
		t.Fatalf("CreateLoan: %v", err)
	}

	remaining, err := debt.RemainingAmount(ctx, gormDB, debtRow.ID)
	if err != nil {
		t.Fatalf("RemainingAmount (after loan): %v", err)
	}
	if remaining != 500_000 {
		t.Errorf("remaining after loan = %.2f, want 500000.00", remaining)
	}

	if _, _, err := txSvc.CreateRepayment(ctx, user.ID, user.DefaultCurrency, "Budi", "received", 200_000, "IDR", "", "", ""); err != nil {
		t.Fatalf("CreateRepayment (partial): %v", err)
	}
	remaining, err = debt.RemainingAmount(ctx, gormDB, debtRow.ID)
	if err != nil {
		t.Fatalf("RemainingAmount (after partial repayment): %v", err)
	}
	if remaining != 300_000 {
		t.Errorf("remaining after partial repayment = %.2f, want 300000.00", remaining)
	}

	if _, _, err := txSvc.CreateRepayment(ctx, user.ID, user.DefaultCurrency, "Budi", "received", 300_000, "IDR", "", "", ""); err != nil {
		t.Fatalf("CreateRepayment (final): %v", err)
	}
	remaining, err = debt.RemainingAmount(ctx, gormDB, debtRow.ID)
	if err != nil {
		t.Fatalf("RemainingAmount (after final repayment): %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining after final repayment = %.2f, want 0.00", remaining)
	}
}

// TestCreateRepayment_NoMatchingDebtReturnsError covers repaying a debt
// direction that was never actually lent/borrowed.
func TestCreateRepayment_NoMatchingDebtReturnsError(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	if _, _, err := txSvc.CreateRepayment(ctx, user.ID, user.DefaultCurrency, "Dania", "made", 100_000, "IDR", "", "", ""); !errors.Is(err, apperror.ErrDebtNotFound) {
		t.Errorf("CreateRepayment err = %v, want ErrDebtNotFound", err)
	}
}

// TestListOpen_HidesFullyRepaidDebtKeepsPartial covers the "computed-away,
// not deleted" list filter — a fully repaid debt must disappear from
// ListOpen while one that's still partially outstanding must remain.
func TestListOpen_HidesFullyRepaidDebtKeepsPartial(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	if _, _, err := txSvc.CreateLoan(ctx, user.ID, user.DefaultCurrency, "Budi", models.DebtOwedToMe, 100_000, "IDR", "", "", ""); err != nil {
		t.Fatalf("CreateLoan(Budi): %v", err)
	}
	if _, _, err := txSvc.CreateRepayment(ctx, user.ID, user.DefaultCurrency, "Budi", "received", 100_000, "IDR", "", "", ""); err != nil {
		t.Fatalf("CreateRepayment(Budi, full): %v", err)
	}

	if _, _, err := txSvc.CreateLoan(ctx, user.ID, user.DefaultCurrency, "Dania", models.DebtIOwe, 200_000, "IDR", "", "", ""); err != nil {
		t.Fatalf("CreateLoan(Dania): %v", err)
	}

	open, err := debt.ListOpen(ctx, gormDB, user.ID)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(open) != 1 {
		names := make([]string, len(open))
		for i, d := range open {
			names[i] = d.Debt.Counterparty
		}
		t.Fatalf("expected only the still-outstanding Dania debt, got %d: %v", len(open), names)
	}
	if open[0].Debt.Counterparty != "Dania" {
		t.Errorf("Counterparty = %q, want %q", open[0].Debt.Counterparty, "Dania")
	}
	if open[0].Remaining != 200_000 {
		t.Errorf("Remaining = %.2f, want 200000.00", open[0].Remaining)
	}
}

// TestUndoLoan_ReducesRemainingAmountAutomatically confirms the headline
// design claim: undoing a loan transaction needs NO special-case cascade
// code — RemainingAmount recomputes live from whatever transactions are
// still active.
func TestUndoLoan_ReducesRemainingAmountAutomatically(t *testing.T) {
	gormDB := setupDB(t)
	ctx := context.Background()
	user := newTestUser(t, gormDB)
	txSvc := transaction.NewService(gormDB, fx.NewStaticService())

	tx, debtRow, err := txSvc.CreateLoan(ctx, user.ID, user.DefaultCurrency, "Budi", models.DebtOwedToMe, 500_000, "IDR", "", "", "")
	if err != nil {
		t.Fatalf("CreateLoan: %v", err)
	}

	if _, err := txSvc.DeleteTransaction(ctx, user.ID, tx.ID); err != nil {
		t.Fatalf("DeleteTransaction (undo the loan): %v", err)
	}

	remaining, err := debt.RemainingAmount(ctx, gormDB, debtRow.ID)
	if err != nil {
		t.Fatalf("RemainingAmount: %v", err)
	}
	if remaining != 0 {
		t.Errorf("remaining after undoing the only loan = %.2f, want 0.00", remaining)
	}
}
