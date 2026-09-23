package models

import "time"

// QuotaUsage is a daily-granularity durable mirror of the Redis quota
// counter. Redis is the enforcement fast-path; this table is the
// audit/analytics fallback (e.g. if Redis is flushed, or for billing
// dispute resolution).
type QuotaUsage struct {
	ID            uint      `gorm:"primaryKey"`
	UserID        uint      `gorm:"not null;uniqueIndex:idx_user_date"`
	UsageDate     time.Time `gorm:"type:date;not null;uniqueIndex:idx_user_date"`
	SuccessCount  int       `gorm:"default:0"`
	RejectedCount int       `gorm:"default:0"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
