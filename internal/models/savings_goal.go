package models

import (
	"time"

	"gorm.io/gorm"
)

// SavingsGoal is a user-defined savings target — "save 10 million for a
// laptop by December" — tracked against real savings activity (the same
// Needs/Wants/Savings bucket definition budget.Service already computes,
// via budget.Service.SavingsSince) rather than manually-logged
// contributions tied to this specific goal. Only one goal is active per
// user at a time, mirroring Budget's own one-active-row pattern above —
// see internal/service/savingsgoal.Service.Create, which deactivates the
// previous goal (if any) rather than deleting it, preserving history.
type SavingsGoal struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint `gorm:"not null;index"`

	// Name is the user's own free-text label for what the goal is for
	// (e.g. "laptop baru", "dana darurat"), kept in whatever language they
	// used — unlike a transaction's CategoryName, this is shown back to the
	// user verbatim, never translated to English.
	Name         string  `gorm:"size:255;not null"`
	TargetAmount float64 `gorm:"type:numeric(18,2);not null"`
	Currency     string  `gorm:"size:3;not null"`

	// Deadline is optional — a goal doesn't need one.
	Deadline *time.Time
	IsActive bool `gorm:"not null;default:true;index"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}
