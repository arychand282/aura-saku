package models

import (
	"time"

	"gorm.io/gorm"
)

type TransactionType string

const (
	TxExpense    TransactionType = "expense"
	TxIncome     TransactionType = "income"
	TxInvestment TransactionType = "investment"
	TxTransfer   TransactionType = "transfer"
	TxReward     TransactionType = "reward"
)

type TransactionSource string

const (
	SourceText  TransactionSource = "text"
	SourceVoice TransactionSource = "voice"
	SourceSync  TransactionSource = "sync_adjustment"
)

// Transaction stores both the original amount/currency as reported by the
// user and the converted amount in the user's default currency (using the
// historical exchange rate at the time of the transaction), so monthly
// aggregation is always done in a single consistent currency without ever
// mutating the original recorded values.
// AccountID is nullable rather than required: it's always set by the
// application (via internal/service/account.GetOrCreateDefault) going
// forward, but nullable avoids a risky NOT NULL migration on a table that
// may already have rows, mirroring the CategoryID pattern below.
//
// Future multi-account note: when a second account becomes
// selectable, a "transfer" between two of the user's own accounts should be
// recorded as two linked Transaction rows (an outflow from the source
// account, an inflow to the destination) rather than adding a ToAccountID
// column — at that point a LinkedTransactionID field will be needed so
// /undo can revert both legs atomically. Not added yet since there's no
// caller that can create a second account.
type Transaction struct {
	ID         uint  `gorm:"primaryKey"`
	UserID     uint  `gorm:"not null;index"`
	AccountID  *uint `gorm:"index"`
	CategoryID *uint `gorm:"index"`

	Type        TransactionType `gorm:"size:15;not null;index"`
	Description string          `gorm:"size:500"`

	// Bucket is a per-transaction Needs/Wants/Savings override, set from the
	// AI's classification of this specific message's context (see
	// ai.ParsedTransaction.Bucket) rather than Category.Bucket's static
	// per-category default. nil means "no override" — every existing row
	// and every non-AI-created transaction (e.g. reconciliation balance
	// adjustments) naturally has this unset, falling back to the category's
	// bucket; see budget.Service.sumSpendByBucket for the fallback order.
	Bucket *BudgetBucket `gorm:"size:10;check:chk_transactions_bucket,bucket IS NULL OR bucket IN ('needs','wants','savings','none')"`

	OriginalAmount   float64 `gorm:"type:numeric(18,2);not null"`
	OriginalCurrency string  `gorm:"size:3;not null"`
	ExchangeRate     float64 `gorm:"type:numeric(18,8);not null;default:1"`
	ConvertedAmount  float64 `gorm:"type:numeric(18,2);not null"`

	Source     TransactionSource `gorm:"size:20;not null;default:'text'"`
	RawInput   string            `gorm:"type:text"`
	OccurredAt time.Time         `gorm:"not null"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	User     User      `gorm:"foreignKey:UserID"`
	Account  *Account  `gorm:"foreignKey:AccountID"`
	Category *Category `gorm:"foreignKey:CategoryID"`
}
