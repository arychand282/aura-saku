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

	// TxLoanOut/TxLoanIn/TxRepaymentIn/TxRepaymentOut back debt tracking
	// (transaction.Service.CreateLoan/CreateRepayment, see Debt) — each is a
	// plain cash movement on ONE of the user's own accounts, tagged via
	// Transaction.DebtID with who it's with. Deliberately excluded from
	// budget.Service (which sums strictly TxExpense) and query_spending's
	// fixed type enumeration, so lending/borrowing/repaying never pollutes a
	// spending report or the Needs/Wants/Savings evaluation — the same
	// "not in that list" exclusion TxTransfer/TxInvestment already get.
	TxLoanOut      TransactionType = "loan_out"
	TxLoanIn       TransactionType = "loan_in"
	TxRepaymentIn  TransactionType = "repayment_in"
	TxRepaymentOut TransactionType = "repayment_out"
)

type TransactionSource string

const (
	SourceText      TransactionSource = "text"
	SourceVoice     TransactionSource = "voice"
	SourcePhoto     TransactionSource = "photo"
	SourceSync      TransactionSource = "sync_adjustment"
	SourceRecurring TransactionSource = "recurring"
)

// TransactionTransferLeg distinguishes the two sides of an inter-account
// transfer created by transaction.Service.CreateTransfer (see
// move_money/ai.IntentAccountTransfer) — nil for every other transaction,
// including the plain external transfer="type" case (e.g. "transfer ke
// adek 500rb"), which still just decreases the single account's balance
// exactly as before this field existed.
type TransactionTransferLeg string

const (
	TransferLegOut TransactionTransferLeg = "out"
	TransferLegIn  TransactionTransferLeg = "in"
)

// Transaction stores both the original amount/currency as reported by the
// user and the converted amount in the user's default currency (using the
// historical exchange rate at the time of the transaction), so monthly
// aggregation is always done in a single consistent currency without ever
// mutating the original recorded values.
// AccountID is nullable rather than required: it's always set by the
// application (via internal/service/account.GetOrCreateDefault or
// GetOrCreateByName) going forward, but nullable avoids a risky NOT NULL
// migration on a table that may already have rows, mirroring the
// CategoryID pattern below.
//
// Multi-account transfers: moving money between two of the user's own
// accounts (transaction.Service.CreateTransfer) records TWO linked rows —
// an "out" leg on the source account and an "in" leg on the destination,
// both Type=transfer, linked via LinkedTransactionID (each points at the
// other) and distinguished by TransferLeg. account.Balance/TotalBalance
// use TransferLeg to sign each leg oppositely, so the pair always nets to
// zero across the user's total balance while correctly shifting each
// individual account's own balance. LinkedTransactionID also lets
// transaction.Service.undoTransaction revert both legs atomically when
// either one is undone/corrected/deleted.
type Transaction struct {
	ID                  uint                    `gorm:"primaryKey"`
	UserID              uint                    `gorm:"not null;index"`
	AccountID           *uint                   `gorm:"index"`
	CategoryID          *uint                   `gorm:"index"`
	LinkedTransactionID *uint                   `gorm:"index"`
	TransferLeg         *TransactionTransferLeg `gorm:"size:10;check:chk_transactions_transfer_leg,transfer_leg IS NULL OR transfer_leg IN ('in','out')"`
	// DebtID links a TxLoanOut/TxLoanIn/TxRepaymentIn/TxRepaymentOut row to
	// the Debt it's with — see Debt's own doc comment for why its remaining
	// amount is computed from these rather than stored.
	DebtID *uint `gorm:"index"`

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
