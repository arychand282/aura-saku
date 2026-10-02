package models

import (
	"time"

	"gorm.io/gorm"
)

// DebtDirection distinguishes who owes whom. DebtOwedToMe: the user lent
// money and is owed it back. DebtIOwe: the user borrowed money and owes it
// back.
type DebtDirection string

const (
	DebtOwedToMe DebtDirection = "owed_to_me"
	DebtIOwe     DebtDirection = "i_owe"
)

// Debt tracks an outstanding loan with a specific person (Counterparty) —
// created by transaction.Service.CreateLoan (ai.IntentDebtLend) and reduced
// by CreateRepayment (ai.IntentDebtRepay). Deliberately carries no
// amount/currency field of its own: the remaining amount is always
// computed live from the ConvertedAmount of every Transaction linked to it
// via Transaction.DebtID (see internal/service/debt.RemainingAmount),
// exactly like account.Balance is computed from a transaction sum rather
// than a mutable running total — undoing/correcting a loan or repayment
// transaction then "just works," with no special-case code needed to keep
// a stored total in sync.
//
// Counterparty is matched case-insensitively (see
// internal/service/debt.GetOrCreateByCounterparty/FindByCounterparty) but
// stored with its original casing, exactly like Account.Name — it's shown
// back to the user, unlike a Category's internal-only name.
type Debt struct {
	ID           uint          `gorm:"primaryKey"`
	UserID       uint          `gorm:"not null;index"`
	Counterparty string        `gorm:"size:100;not null"`
	Direction    DebtDirection `gorm:"size:15;not null;check:chk_debts_direction,direction IN ('owed_to_me','i_owe')"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`

	User User `gorm:"foreignKey:UserID"`
}
