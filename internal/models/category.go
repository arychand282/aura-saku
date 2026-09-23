package models

import "time"

type BudgetBucket string

const (
	BucketNeeds   BudgetBucket = "needs"
	BucketWants   BudgetBucket = "wants"
	BucketSavings BudgetBucket = "savings"
	BucketNone    BudgetBucket = "none"
)

type Category struct {
	ID        uint         `gorm:"primaryKey"`
	Name      string       `gorm:"size:100;uniqueIndex;not null"`
	Bucket    BudgetBucket `gorm:"size:10;not null;default:'none'"`
	IsSystem  bool         `gorm:"default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
