package models

import (
	"time"

	"gorm.io/gorm"
)

// AdminRole distinguishes dashboard operator permission levels. Superadmin
// vs admin scope isn't enforced anywhere yet beyond carrying the role
// through the session — specific route-level restrictions get added as
// those admin-only features are built.
type AdminRole string

const (
	AdminRoleAdmin      AdminRole = "admin"
	AdminRoleSuperadmin AdminRole = "superadmin"
)

// Admin is a dashboard operator account — separate from User (Telegram bot
// users), since an admin isn't necessarily a bot user at all and needs
// direct-browser credentials rather than a Telegram-issued identity. A
// database table (not a single .env credential) so more than one admin can
// be added later without a code change.
type Admin struct {
	ID           uint      `gorm:"primaryKey"`
	Email        string    `gorm:"size:255;uniqueIndex;not null"`
	Name         string    `gorm:"size:100;not null"`
	PasswordHash string    `gorm:"size:255;not null"`
	Role         AdminRole `gorm:"size:20;not null;default:'admin'"`
	IsActive     bool      `gorm:"default:true"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}
