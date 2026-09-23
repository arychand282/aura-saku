package models

import (
	"time"

	"gorm.io/gorm"
)

// Budget stores a user's customizable 50/30/20 allocation. The service layer
// is responsible for validating that the three percentages sum to 100 — the
// DB layer intentionally does not enforce this via a CHECK constraint so
// intermediate/partial edits from a future settings UI don't get rejected at
// the storage layer.
type Budget struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint `gorm:"not null;index"`

	NeedsPercent   float64 `gorm:"type:numeric(5,2);not null;default:50"`
	WantsPercent   float64 `gorm:"type:numeric(5,2);not null;default:30"`
	SavingsPercent float64 `gorm:"type:numeric(5,2);not null;default:20"`

	PeriodStart time.Time `gorm:"not null"`
	IsActive    bool      `gorm:"default:true"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}
