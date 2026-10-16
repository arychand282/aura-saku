package handlers

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// TransactionRow is the flat, JSON-friendly shape shared by
// TransactionHandler.List and ExportHandler — LEFT JOINs to categories and
// accounts, instead of raw GORM Transaction structs whose unpreloaded User/
// Account/Category associations serialize as confusing zero-valued nested
// objects.
type TransactionRow struct {
	ID               uint      `json:"id"`
	OccurredAt       time.Time `json:"occurred_at"`
	Type             string    `json:"type"`
	CategoryName     string    `json:"category_name"`
	AccountName      string    `json:"account_name"`
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
// searchTerm, when non-empty, ILIKE-matches Description OR RawInput — the
// same free-text search convention report.Service.ListTransactions already
// uses for the bot's own "cari transaksi X" natural-language path; empty
// applies no filter at all. Callers must clamp limit/offset themselves —
// not sanitized here.
func fetchTransactionRows(ctx context.Context, db *gorm.DB, userID uint, searchTerm string, limit, offset int) ([]TransactionRow, error) {
	query := db.WithContext(ctx).Table("transactions").
		Select(`transactions.id, transactions.occurred_at, transactions.type,
			COALESCE(categories.name, '') as category_name,
			COALESCE(accounts.name, '') as account_name,
			transactions.description,
			transactions.original_amount, transactions.original_currency,
			transactions.exchange_rate, transactions.converted_amount, transactions.source,
			transactions.raw_input`).
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Joins("LEFT JOIN accounts ON accounts.id = transactions.account_id").
		Where("transactions.user_id = ?", userID)

	if searchTerm != "" {
		query = query.Where("(transactions.description ILIKE ? OR transactions.raw_input ILIKE ?)", "%"+searchTerm+"%", "%"+searchTerm+"%")
	}

	var rows []TransactionRow
	err := query.
		Order("transactions.occurred_at DESC").
		Limit(limit).
		Offset(offset).
		Scan(&rows).Error
	return rows, err
}

// countTransactionRows mirrors fetchTransactionRows' filtering (minus the
// join, since the search filter only ever touches columns already on
// transactions itself) so List's pagination total always matches what the
// same search would actually return.
func countTransactionRows(ctx context.Context, db *gorm.DB, userID uint, searchTerm string) (int64, error) {
	query := db.WithContext(ctx).Table("transactions").Where("user_id = ?", userID)
	if searchTerm != "" {
		query = query.Where("(description ILIKE ? OR raw_input ILIKE ?)", "%"+searchTerm+"%", "%"+searchTerm+"%")
	}
	var count int64
	err := query.Count(&count).Error
	return count, err
}
