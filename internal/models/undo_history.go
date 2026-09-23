package models

import "time"

// UndoHistory records every mutating transaction command outcome so /undo
// (and potential future /undo N, /history) has a durable trail beyond what
// a single soft-deleted Transaction row can express.
type UndoHistory struct {
	ID            uint      `gorm:"primaryKey"`
	UserID        uint      `gorm:"not null;index"`
	TransactionID uint      `gorm:"not null;index"`
	Action        string    `gorm:"size:20;not null"`
	SnapshotJSON  string    `gorm:"type:jsonb"`
	CreatedAt     time.Time `gorm:"index"`
}
