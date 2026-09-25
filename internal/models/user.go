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

// Language is the user's preferred bot reply language. Defaults to
// Indonesian (the primary market) when undetectable; auto-detected from
// Telegram's own language_code on first contact, overridable via /language.
type Language string

const (
	LanguageID Language = "id"
	LanguageEN Language = "en"
)

type User struct {
	ID               uint     `gorm:"primaryKey"`
	TelegramChatID   int64    `gorm:"uniqueIndex;not null"`
	TelegramUsername string   `gorm:"size:100"`
	FirstName        string   `gorm:"size:100"`
	DefaultCurrency  string   `gorm:"size:3;not null;default:'IDR'"`
	Language         Language `gorm:"size:5;not null;default:'id'"`
	// check tag mirrors TierFree/TierPro above at the DB level — keep both in
	// sync if a tier is ever added or renamed.
	Tier           SubscriptionTier `gorm:"size:10;not null;default:'free';check:chk_users_tier,tier IN ('free','pro')"`
	TimezoneOffset int              `gorm:"default:0"`
	IsActive       bool             `gorm:"default:true"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`

	Accounts        []Account               `gorm:"foreignKey:UserID"`
	Subscriptions   []Subscription          `gorm:"foreignKey:UserID"`
	Transactions    []Transaction           `gorm:"foreignKey:UserID"`
	Budgets         []Budget                `gorm:"foreignKey:UserID"`
	Reconciliations []BalanceReconciliation `gorm:"foreignKey:UserID"`
}
