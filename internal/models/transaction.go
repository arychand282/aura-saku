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
type Transaction struct {
	ID         uint  `gorm:"primaryKey"`
	UserID     uint  `gorm:"not null;index"`
	CategoryID *uint `gorm:"index"`

	Type        TransactionType `gorm:"size:15;not null;index"`
	Description string          `gorm:"size:500"`

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
	Category *Category `gorm:"foreignKey:CategoryID"`
}
