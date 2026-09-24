package models

import (
	"time"

	"gorm.io/gorm"
)

type SubscriptionTier string

const (
	TierFree SubscriptionTier = "free"
	TierPro  SubscriptionTier = "pro"
)

type User struct {
	ID               uint             `gorm:"primaryKey"`
	TelegramChatID   int64            `gorm:"uniqueIndex;not null"`
	TelegramUsername string           `gorm:"size:100"`
	FirstName        string           `gorm:"size:100"`
	DefaultCurrency  string           `gorm:"size:3;not null;default:'IDR'"`
	Tier             SubscriptionTier `gorm:"size:10;not null;default:'free'"`
	TimezoneOffset   int              `gorm:"default:0"`
	IsActive         bool             `gorm:"default:true"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`

	Accounts        []Account               `gorm:"foreignKey:UserID"`
	Subscriptions   []Subscription          `gorm:"foreignKey:UserID"`
	Transactions    []Transaction           `gorm:"foreignKey:UserID"`
	Budgets         []Budget                `gorm:"foreignKey:UserID"`
	Reconciliations []BalanceReconciliation `gorm:"foreignKey:UserID"`
}
