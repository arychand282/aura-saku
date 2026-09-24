package handlers

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"

	"aura-saku/pkg/httpresponse"
)

// exportMaxRows caps how much history a single export can pull, as a safety
// limit rather than a product constraint — personal expense history is not
// expected to approach this in practice.
const exportMaxRows = 5000

var exportColumns = []string{
	"ID", "Date", "Type", "Category", "Description",
	"Original Amount", "Original Currency", "Exchange Rate",
	"Converted Amount", "Source",
}

type ExportHandler struct {
	db *gorm.DB
}

func NewExportHandler(db *gorm.DB) *ExportHandler {
	return &ExportHandler{db: db}
}

type exportRow struct {
	ID               uint
	OccurredAt       time.Time
	Type             string
	CategoryName     string
	Description      string
	OriginalAmount   float64
	OriginalCurrency string
	ExchangeRate     float64
	ConvertedAmount  float64
	Source           string
}

// Export streams a user's full transaction history as CSV or XLSX.
//
// TODO(auth): once the dashboard has real session auth, derive user_id from
// the authenticated session instead of trusting a query param (same
// limitation as TransactionHandler.List).
func (h *ExportHandler) Export(c *fiber.Ctx) error {
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "user_id query param is required")
	}

	format := c.Query("format", "csv")
	if format != "csv" && format != "xlsx" {
		return httpresponse.Error(c, fiber.StatusBadRequest, "format must be csv or xlsx")
	}

	rows, err := h.fetchRows(c.Context(), userID)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to fetch transactions for export")
	}

	filename := fmt.Sprintf("aura-saku-transactions-%s.%s", time.Now().UTC().Format("2006-01-02"), format)
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", filename))

	if format == "xlsx" {
		return h.writeXLSX(c, rows)
	}
	return h.writeCSV(c, rows)
}

func (h *ExportHandler) fetchRows(ctx context.Context, userID uint64) ([]exportRow, error) {
	var rows []exportRow
	err := h.db.WithContext(ctx).Table("transactions").
		Select(`transactions.id, transactions.occurred_at, transactions.type,
			COALESCE(categories.name, '') as category_name, transactions.description,
			transactions.original_amount, transactions.original_currency,
			transactions.exchange_rate, transactions.converted_amount, transactions.source`).
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ?", userID).
		Order("transactions.occurred_at DESC").
		Limit(exportMaxRows).
		Scan(&rows).Error
	return rows, err
}

func toRecord(r exportRow) []string {
	return []string{
		strconv.FormatUint(uint64(r.ID), 10),
		r.OccurredAt.Format(time.RFC3339),
		r.Type,
		r.CategoryName,
		r.Description,
		strconv.FormatFloat(r.OriginalAmount, 'f', 2, 64),
		r.OriginalCurrency,
		strconv.FormatFloat(r.ExchangeRate, 'f', 8, 64),
		strconv.FormatFloat(r.ConvertedAmount, 'f', 2, 64),
		r.Source,
	}
}

func (h *ExportHandler) writeCSV(c *fiber.Ctx, rows []exportRow) error {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)

	if err := w.Write(exportColumns); err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to write CSV")
	}
	for _, r := range rows {
		if err := w.Write(toRecord(r)); err != nil {
			return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to write CSV")
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to write CSV")
	}

	c.Set(fiber.HeaderContentType, "text/csv")
	return c.Send(buf.Bytes())
}

func (h *ExportHandler) writeXLSX(c *fiber.Ctx, rows []exportRow) error {
	f := excelize.NewFile()
	defer f.Close()

	const sheet = "Transactions"
	f.SetSheetName("Sheet1", sheet)

	for i, col := range exportColumns {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheet, cell, col)
	}
	for rowIdx, r := range rows {
		record := toRecord(r)
		excelRow := rowIdx + 2
		for colIdx, val := range record {
			cell, _ := excelize.CoordinatesToCellName(colIdx+1, excelRow)
			f.SetCellValue(sheet, cell, val)
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to write XLSX")
	}

	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	return c.Send(buf.Bytes())
}
