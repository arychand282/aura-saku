package handlers

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/i18n"
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
	userID := effectiveUserID(c)

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
	userID := effectiveUserID(c)

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

// BudgetProgress returns the user's current-month 50/30/20 status plus a
// rollover suggestion for last month's surplus, if any. Warning messages are
// localized to the user's stored Language, unless a `lang` query param
// overrides it for this request (e.g. a dashboard viewed in a different
// language than the user's bot chat preference).
func (h *ReportHandler) BudgetProgress(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	lang := h.resolveLanguage(c, userID)

	status, err := h.budgetSvc.EvaluateBudget(c.Context(), userID, lang)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to evaluate budget")
	}

	// Rollover suggestion is a nice-to-have on top of the core budget
	// status; a failure here shouldn't fail the whole response.
	rollover, err := h.budgetSvc.SuggestRollover(c.Context(), userID, lang)
	if err != nil {
		rollover = ""
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"status":              status,
		"rollover_suggestion": rollover,
	})
}

func (h *ReportHandler) resolveLanguage(c *fiber.Ctx, userID uint) models.Language {
	if lang, ok := i18n.ParseCommandInput(c.Query("lang")); ok {
		return lang
	}
	var user models.User
	if err := h.db.Select("language").First(&user, userID).Error; err == nil && user.Language != "" {
		return user.Language
	}
	return models.LanguageID
}
