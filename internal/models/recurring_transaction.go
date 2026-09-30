package models

import (
	"time"

	"gorm.io/gorm"
)

// RecurringTransaction is a user-defined transaction — rent, a subscription,
// a salary deposit, and the like — that fires once a month instead of
// requiring a fresh message every time. Two modes, both sharing the exact
// same schedule (day-of-month, month-rollover/clamping math, NextRunAt
// advancement — see internal/service/recurring.Service): if ReminderOnly is
// false (the default, and the only behavior that existed before that field
// did), it auto-records a real Transaction each time it fires. If true,
// it's a bill reminder instead — RunRecurringTransactions only messages the
// user ("this is due today") and records nothing, since the user still
// wants to log the actual payment themselves. See
// internal/service/bot.Service.RunRecurringTransactions (the
// internal/scheduler job that acts on these) for both branches.
type RecurringTransaction struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint `gorm:"not null;index"`

	// Type/Amount/Currency/CategoryName/Description/Bucket mirror
	// ai.ParsedTransaction's own record-transaction fields exactly —
	// RunRecurringTransactions reconstructs an ai.ParsedTransaction from
	// these to reuse transaction.Service.CreateFromParsed unchanged rather
	// than duplicating its category-resolution/FX-conversion logic for a
	// second "recurring" code path.
	Type         TransactionType `gorm:"size:15;not null"`
	Amount       float64         `gorm:"type:numeric(18,2);not null"`
	Currency     string          `gorm:"size:3;not null"`
	CategoryName string          `gorm:"size:100;not null"`
	Description  string          `gorm:"size:500"`
	Bucket       *BudgetBucket   `gorm:"size:10;check:chk_recurring_transactions_bucket,bucket IS NULL OR bucket IN ('needs','wants','savings','none')"`

	// DayOfMonth is 1-31 as the user stated it — a month shorter than this
	// (e.g. 31 in April, or in February) uses that month's own last day
	// instead at schedule-computation time (see recurring.Service), so no
	// clamping happens here at write time.
	DayOfMonth int `gorm:"not null"`

	// NextRunAt is the next UTC midnight this should fire, always exactly
	// DayOfMonth in its own month (clamped for short months) — advanced by
	// one calendar month from itself (not recomputed from "now") each time
	// RunRecurringTransactions posts it, so the schedule never drifts even
	// if the scheduler was down for a while. Indexed since the job's whole
	// query is "everything due now".
	NextRunAt time.Time `gorm:"not null;index"`
	IsActive  bool      `gorm:"not null;default:true;index"`

	// ReminderOnly, see the type doc comment above: false auto-records,
	// true just notifies. Defaults false so every row created before this
	// field existed keeps its original auto-recording behavior unchanged.
	ReminderOnly bool `gorm:"not null;default:false"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}
