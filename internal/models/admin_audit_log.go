package models

import "time"

// AdminAuditLog records every dashboard request an admin/superadmin makes on
// behalf of another user (via ?user_id=). Since this is financial data and
// more than one admin can exist, "who looked at whose data, and when" needs
// to be reconstructable — not soft-deletable, an audit trail must be
// append-only.
type AdminAuditLog struct {
	ID           uint   `gorm:"primaryKey"`
	AdminID      uint   `gorm:"not null;index"`
	TargetUserID uint   `gorm:"not null;index"`
	Path         string `gorm:"size:255;not null"`
	CreatedAt    time.Time
}
