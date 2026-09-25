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
	ID uint `gorm:"primaryKey"`
	// check tag enforces lowercase at the DB level so no insert path —
	// including a future raw-SQL seed, not just transaction.resolveCategory
	// — can ever create a case-duplicate of an existing category again.
	Name      string       `gorm:"size:100;uniqueIndex;not null;check:chk_categories_name_lowercase,name = lower(name)"`
	Bucket    BudgetBucket `gorm:"size:10;not null;default:'none'"`
	IsSystem  bool         `gorm:"default:true"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
