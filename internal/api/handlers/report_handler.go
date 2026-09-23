package handlers

import (
	"errors"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
	"aura-saku/internal/service/budget"
	"aura-saku/pkg/httpresponse"
)

type ReportHandler struct {
	db        *gorm.DB
	budgetSvc *budget.Service
}

func NewReportHandler(db *gorm.DB, budgetSvc *budget.Service) *ReportHandler {
	return &ReportHandler{db: db, budgetSvc: budgetSvc}
}

func periodRange(period string) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	switch period {
	case "daily":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), now, nil
	case "weekly":
		return now.AddDate(0, 0, -7), now, nil
	case "monthly":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), now, nil
	case "yearly":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC), now, nil
	default:
		return time.Time{}, time.Time{}, errors.New("invalid period, must be one of: daily, weekly, monthly, yearly")
	}
}

// Summary aggregates a user's transaction totals by type over the requested period.
func (h *ReportHandler) Summary(c *fiber.Ctx) error {
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "user_id query param is required")
	}

	period := c.Query("period", "monthly")
	start, end, err := periodRange(period)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, err.Error())
	}

	type totalRow struct {
		Type  string
		Total float64
	}
	var rows []totalRow
	result := h.db.Model(&models.Transaction{}).
		Select("type, COALESCE(SUM(converted_amount), 0) as total").
		Where("user_id = ? AND occurred_at BETWEEN ? AND ?", userID, start, end).
		Group("type").
		Scan(&rows)
	if result.Error != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to compute summary")
	}

	totals := map[string]float64{}
	for _, r := range rows {
		totals[r.Type] = r.Total
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"period":      period,
		"range_start": start,
		"range_end":   end,
		"totals":      totals,
	})
}

// TopCategories returns the top 5 expense categories by converted amount over the requested period.
func (h *ReportHandler) TopCategories(c *fiber.Ctx) error {
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "user_id query param is required")
	}

	period := c.Query("period", "monthly")
	start, end, err := periodRange(period)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, err.Error())
	}

	type categoryRow struct {
		CategoryName string
		Total        float64
	}
	var rows []categoryRow
	result := h.db.Table("transactions").
		Select("categories.name as category_name, COALESCE(SUM(transactions.converted_amount), 0) as total").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ? AND transactions.type = ? AND transactions.occurred_at BETWEEN ? AND ?", userID, models.TxExpense, start, end).
		Group("categories.name").
		Order("total DESC").
		Limit(5).
		Scan(&rows)
	if result.Error != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to compute top categories")
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"period":         period,
		"top_categories": rows,
	})
}

// BudgetProgress delegates to budget.Service, which is TODO-stubbed for now.
func (h *ReportHandler) BudgetProgress(c *fiber.Ctx) error {
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "user_id query param is required")
	}

	status, err := h.budgetSvc.EvaluateBudget(c.Context(), uint(userID))
	if err != nil {
		if errors.Is(err, apperror.ErrNotImplemented) {
			return httpresponse.Error(c, fiber.StatusNotImplemented, "budget progress is not wired up yet")
		}
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to evaluate budget")
	}

	return httpresponse.Success(c, fiber.StatusOK, status)
}
