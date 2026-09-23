package db

import (
	"gorm.io/gorm"

	"aura-saku/internal/models"
)

// RunMigrations auto-migrates every domain model. Order matters for FK
// creation: User must exist before anything referencing UserID, Category
// before Transaction.
func RunMigrations(gormDB *gorm.DB) error {
	return gormDB.AutoMigrate(
		&models.User{},
		&models.Subscription{},
		&models.Category{},
		&models.Transaction{},
		&models.Budget{},
		&models.BalanceReconciliation{},
		&models.AIUsageLog{},
		&models.QuotaUsage{},
		&models.UndoHistory{},
	)
}
