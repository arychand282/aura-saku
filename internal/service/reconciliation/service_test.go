package reconciliation_test

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
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/transaction"
)

// TestSync is an integration test against a real Postgres instance (see
// docker-compose.yml). It's skipped automatically if no DB is reachable, so
// `go test ./...` still passes without the local dev stack.
func TestSync(t *testing.T) {
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
	reconSvc := reconciliation.NewService(gormDB, txSvc)

	// Income 1,000,000 - expense 200,000 => calculated balance 800,000.
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 1_000_000, Currency: "IDR", CategoryName: "salary",
	}, models.SourceText, "salary 1000000"); err != nil {
		t.Fatalf("create income: %v", err)
	}
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 200_000, Currency: "IDR", CategoryName: "food",
	}, models.SourceText, "food 200000"); err != nil {
		t.Fatalf("create expense: %v", err)
	}

	matched, err := reconSvc.Sync(ctx, user.ID, 800_000)
	if err != nil {
		t.Fatalf("Sync (matched case): %v", err)
	}
	if matched.Status != models.ReconMatched {
		t.Fatalf("expected status matched, got %s (discrepancy %.2f)", matched.Status, matched.Discrepancy)
	}
	if matched.SuggestedAdjustmentTxID != nil {
		t.Fatal("expected no adjustment transaction on a matched sync")
	}

	// Actual balance is 50,000 higher than calculated -> unrecorded income.
	adjusted, err := reconSvc.Sync(ctx, user.ID, 850_000)
	if err != nil {
		t.Fatalf("Sync (discrepancy case): %v", err)
	}
	if adjusted.Status != models.ReconAdjusted {
		t.Fatalf("expected status adjusted, got %s", adjusted.Status)
	}
	if adjusted.Discrepancy != 50_000 {
		t.Fatalf("expected discrepancy 50,000, got %.2f", adjusted.Discrepancy)
	}
	if adjusted.SuggestedAdjustmentTxID == nil {
		t.Fatal("expected an adjustment transaction to be created")
	}

	var adjustmentTx models.Transaction
	if err := gormDB.First(&adjustmentTx, *adjusted.SuggestedAdjustmentTxID).Error; err != nil {
		t.Fatalf("fetch adjustment transaction: %v", err)
	}
	if adjustmentTx.Type != models.TxIncome {
		t.Fatalf("expected adjustment transaction to be type income, got %s", adjustmentTx.Type)
	}
	if adjustmentTx.ConvertedAmount != 50_000 {
		t.Fatalf("expected adjustment amount 50,000, got %.2f", adjustmentTx.ConvertedAmount)
	}

	// A follow-up sync at the now-true balance should match with no new adjustment.
	reconciled, err := reconSvc.Sync(ctx, user.ID, 850_000)
	if err != nil {
		t.Fatalf("Sync (post-adjustment case): %v", err)
	}
	if reconciled.Status != models.ReconMatched {
		t.Fatalf("expected status matched after adjustment, got %s (discrepancy %.2f)", reconciled.Status, reconciled.Discrepancy)
	}
}

func cleanup(t *testing.T, gormDB *gorm.DB, userID uint) {
	t.Helper()
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.UndoHistory{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Transaction{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.BalanceReconciliation{})
	gormDB.Unscoped().Where("user_id = ?", userID).Delete(&models.Account{})
	gormDB.Unscoped().Where("id = ?", userID).Delete(&models.User{})
}
