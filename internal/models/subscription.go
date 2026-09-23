package models

import (
	"time"

	"gorm.io/gorm"
)

type SubscriptionStatus string

const (
	SubStatusActive   SubscriptionStatus = "active"
	SubStatusExpired  SubscriptionStatus = "expired"
	SubStatusCanceled SubscriptionStatus = "canceled"
)

type Subscription struct {
	ID         uint               `gorm:"primaryKey"`
	UserID     uint               `gorm:"not null;index"`
	Tier       SubscriptionTier   `gorm:"size:10;not null"`
	Status     SubscriptionStatus `gorm:"size:10;not null;default:'active'"`
	StartedAt  time.Time          `gorm:"not null"`
	ExpiresAt  *time.Time
	PaymentRef string `gorm:"size:255"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}
