package models

import "time"

type ReconciliationStatus string

const (
	ReconMatched     ReconciliationStatus = "matched"
	ReconDiscrepancy ReconciliationStatus = "discrepancy"
	ReconAdjusted    ReconciliationStatus = "adjusted"
)

// BalanceReconciliation records the outcome of a /sync command: the user's
// self-reported actual bank balance versus what the app has calculated from
// recorded transactions. A Discrepancy triggers an AI-suggested "blind spot"
// adjustment entry, tracked via SuggestedAdjustmentTxID once created.
type BalanceReconciliation struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint `gorm:"not null;index"`

	ActualBalance     float64              `gorm:"type:numeric(18,2);not null"`
	CalculatedBalance float64              `gorm:"type:numeric(18,2);not null"`
	Discrepancy       float64              `gorm:"type:numeric(18,2);not null"`
	Status            ReconciliationStatus `gorm:"size:20;not null;default:'discrepancy'"`

	SuggestedAdjustmentTxID *uint `gorm:"index"`

	CreatedAt time.Time
}
