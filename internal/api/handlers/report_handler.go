package handlers

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/i18n"
	"aura-saku/internal/models"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/report"
	"aura-saku/pkg/httpresponse"
)

// respondReportError maps a report.Service error to an HTTP response: an
// invalid period is a client mistake (400, safe to echo verbatim back); any
// other failure is a real query error (500, generic message — the raw GORM
// error isn't safe to hand back to the client).
func respondReportError(c *fiber.Ctx, err error) error {
	if errors.Is(err, apperror.ErrInvalidPeriod) {
		return httpresponse.Error(c, fiber.StatusBadRequest, err.Error())
	}
	return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to compute report")
}

type ReportHandler struct {
	db        *gorm.DB
	budgetSvc *budget.Service
	reportSvc *report.Service
}

func NewReportHandler(db *gorm.DB, budgetSvc *budget.Service, reportSvc *report.Service) *ReportHandler {
	return &ReportHandler{db: db, budgetSvc: budgetSvc, reportSvc: reportSvc}
}

// Summary aggregates a user's transaction totals by type over the requested period.
func (h *ReportHandler) Summary(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	period := c.Query("period", "monthly")
	totals, start, end, err := h.reportSvc.Summary(c.Context(), userID, period)
	if err != nil {
		return respondReportError(c, err)
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
	rows, err := h.reportSvc.TopCategories(c.Context(), userID, period)
	if err != nil {
		return respondReportError(c, err)
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
