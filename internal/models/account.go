package models

import (
	"time"

	"gorm.io/gorm"
)

// Account represents a tracked balance (a bank account, cash wallet,
// e-wallet, etc). Every user currently gets exactly one auto-created
// "Default" account (see internal/service/account) — the model exists now
// so the schema doesn't need to change when multi-account bot/dashboard UX
// is added later.
type Account struct {
	ID        uint   `gorm:"primaryKey"`
	UserID    uint   `gorm:"not null;index"`
	Name      string `gorm:"size:100;not null"`
	IsDefault bool   `gorm:"default:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}
