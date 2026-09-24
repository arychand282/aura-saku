package handlers

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// TransactionRow is the flat, JSON-friendly shape shared by
// TransactionHandler.List and ExportHandler — one LEFT JOIN to categories,
// instead of raw GORM Transaction structs whose unpreloaded User/Account/
// Category associations serialize as confusing zero-valued nested objects.
type TransactionRow struct {
	ID               uint      `json:"id"`
	OccurredAt       time.Time `json:"occurred_at"`
	Type             string    `json:"type"`
	CategoryName     string    `json:"category_name"`
	Description      string    `json:"description"`
	OriginalAmount   float64   `json:"original_amount"`
	OriginalCurrency string    `json:"original_currency"`
	ExchangeRate     float64   `json:"exchange_rate"`
	ConvertedAmount  float64   `json:"converted_amount"`
	Source           string    `json:"source"`
	RawInput         string    `json:"raw_input"`
}

// fetchTransactionRows is the single query both the paginated dashboard
// list and the full CSV/XLSX export use, so their shapes can never diverge.
// Callers must clamp limit/offset themselves — not sanitized here.
func fetchTransactionRows(ctx context.Context, db *gorm.DB, userID uint, limit, offset int) ([]TransactionRow, error) {
	var rows []TransactionRow
	err := db.WithContext(ctx).Table("transactions").
		Select(`transactions.id, transactions.occurred_at, transactions.type,
			COALESCE(categories.name, '') as category_name, transactions.description,
			transactions.original_amount, transactions.original_currency,
			transactions.exchange_rate, transactions.converted_amount, transactions.source,
			transactions.raw_input`).
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ?", userID).
		Order("transactions.occurred_at DESC").
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error
	return rows, err
}
