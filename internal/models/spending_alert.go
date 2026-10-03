package models

import (
	"time"

	"gorm.io/gorm"
)

// SpendingAlertScope distinguishes what a SpendingAlert's threshold is
// measured against — the same scope vocabulary query_spending's own
// category/bucket filters already use, plus "total" for overall spending
// with no filter at all.
type SpendingAlertScope string

const (
	AlertScopeCategory SpendingAlertScope = "category"
	AlertScopeBucket   SpendingAlertScope = "bucket"
	AlertScopeTotal    SpendingAlertScope = "total"
)

// SpendingAlert is a user-configured standing threshold ("kasih tau kalau
// pengeluaran makan lebih dari 2 juta bulan ini") — checked after every new
// expense (see bot.Service.checkSpendingAlerts), never on a schedule, since
// crossing a threshold only ever happens when a new expense pushes past it.
// ThresholdAmount is always interpreted in the user's own DefaultCurrency —
// there's no per-alert currency/FX conversion (see this feature's own
// design notes for why). CategoryName is only meaningful when
// Scope==AlertScopeCategory; Bucket is only meaningful when
// Scope==AlertScopeBucket; both are left zero-valued for AlertScopeTotal.
type SpendingAlert struct {
	ID              uint               `gorm:"primaryKey"`
	UserID          uint               `gorm:"not null;index"`
	Scope           SpendingAlertScope `gorm:"size:10;not null;check:chk_spending_alerts_scope,scope IN ('category','bucket','total')"`
	CategoryName    string             `gorm:"size:100"`
	Bucket          *BudgetBucket      `gorm:"size:10;check:chk_spending_alerts_bucket,bucket IS NULL OR bucket IN ('needs','wants','savings')"`
	ThresholdAmount float64            `gorm:"type:numeric(18,2);not null"`
	Period          string             `gorm:"size:10;not null;check:chk_spending_alerts_period,period IN ('daily','weekly','monthly','yearly')"`
	IsActive        bool               `gorm:"not null;default:true;index"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}
