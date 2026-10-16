package handlers

import (
	"context"
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
	"aura-saku/internal/service/alert"
	"aura-saku/internal/service/report"
	"aura-saku/pkg/httpresponse"
)

type AlertHandler struct {
	db        *gorm.DB
	reportSvc *report.Service
}

func NewAlertHandler(db *gorm.DB, reportSvc *report.Service) *AlertHandler {
	return &AlertHandler{db: db, reportSvc: reportSvc}
}

type alertRow struct {
	ID              uint    `json:"id"`
	Scope           string  `json:"scope"`
	CategoryName    string  `json:"category_name,omitempty"`
	Bucket          string  `json:"bucket,omitempty"`
	ThresholdAmount float64 `json:"threshold_amount"`
	Period          string  `json:"period"`
	CurrentSpend    float64 `json:"current_spend"`
}

// currentAlertSpend mirrors bot.Service's own private method of the same
// name (internal/service/bot/service.go) — kept as its own small copy
// rather than a cross-package dependency on an unexported bot.Service
// method, the same "small per-package copy over coupling for a few lines
// of mapping logic" convention this codebase already uses elsewhere (see
// e.g. recurring.Service's own resolveBucket doc comment).
func currentAlertSpend(ctx context.Context, reportSvc *report.Service, a models.SpendingAlert) (float64, error) {
	start, end, err := report.PeriodRange(a.Period)
	if err != nil {
		return 0, err
	}
	switch a.Scope {
	case models.AlertScopeCategory:
		total, _, err := reportSvc.CategoryTotalRange(ctx, a.UserID, start, end, a.CategoryName)
		return total, err
	case models.AlertScopeBucket:
		bucket := models.BudgetBucket("")
		if a.Bucket != nil {
			bucket = *a.Bucket
		}
		breakdown, err := reportSvc.BucketSpendingRange(ctx, a.UserID, start, end, bucket)
		if err != nil {
			return 0, err
		}
		return breakdown.Total, nil
	default: // models.AlertScopeTotal
		totals, err := reportSvc.SummaryRange(ctx, a.UserID, start, end)
		if err != nil {
			return 0, err
		}
		return totals[string(models.TxExpense)], nil
	}
}

// List returns every one of the user's active spending alerts, each with
// its own current period-to-date spend computed the exact same way
// bot.Service detects a crossing, so the number shown here always matches
// what would actually trigger a Telegram warning.
func (h *AlertHandler) List(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	alerts, err := alert.ListActive(c.Context(), h.db, userID)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to list alerts")
	}

	rows := make([]alertRow, 0, len(alerts))
	for _, a := range alerts {
		spend, err := currentAlertSpend(c.Context(), h.reportSvc, a)
		if err != nil {
			return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to compute alert spend")
		}
		row := alertRow{
			ID:              a.ID,
			Scope:           string(a.Scope),
			CategoryName:    a.CategoryName,
			ThresholdAmount: a.ThresholdAmount,
			Period:          a.Period,
			CurrentSpend:    spend,
		}
		if a.Bucket != nil {
			row.Bucket = string(*a.Bucket)
		}
		rows = append(rows, row)
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{"alerts": rows})
}

type createAlertRequest struct {
	Scope           string  `json:"scope"`
	CategoryName    string  `json:"category_name"`
	Bucket          string  `json:"bucket"`
	ThresholdAmount float64 `json:"threshold_amount"`
	Period          string  `json:"period"`
}

// Create sets up (or updates in place — see alert.Create) a standing
// spend-threshold alert.
func (h *AlertHandler) Create(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	var req createAlertRequest
	if err := c.BodyParser(&req); err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "invalid request body")
	}

	var scope models.SpendingAlertScope
	var bucketPtr *models.BudgetBucket
	switch req.Scope {
	case "category":
		scope = models.AlertScopeCategory
		if req.CategoryName == "" {
			return httpresponse.Error(c, fiber.StatusBadRequest, "category_name is required for scope=category")
		}
	case "bucket":
		scope = models.AlertScopeBucket
		switch req.Bucket {
		case string(models.BucketNeeds), string(models.BucketWants), string(models.BucketSavings):
			b := models.BudgetBucket(req.Bucket)
			bucketPtr = &b
		default:
			return httpresponse.Error(c, fiber.StatusBadRequest, "bucket must be one of needs, wants, savings")
		}
	case "total":
		scope = models.AlertScopeTotal
	default:
		return httpresponse.Error(c, fiber.StatusBadRequest, "scope must be one of category, bucket, total")
	}

	switch req.Period {
	case "daily", "weekly", "monthly", "yearly":
	default:
		return httpresponse.Error(c, fiber.StatusBadRequest, "period must be one of daily, weekly, monthly, yearly")
	}
	if req.ThresholdAmount <= 0 {
		return httpresponse.Error(c, fiber.StatusBadRequest, "threshold_amount must be positive")
	}

	a, err := alert.Create(c.Context(), h.db, userID, scope, req.CategoryName, bucketPtr, req.ThresholdAmount, req.Period)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to create alert")
	}

	return httpresponse.Success(c, fiber.StatusCreated, fiber.Map{"id": a.ID})
}

// Cancel deactivates one of the user's own active alerts.
func (h *AlertHandler) Cancel(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "invalid alert id")
	}

	if _, err := alert.Cancel(c.Context(), h.db, userID, uint(id)); err != nil {
		if errors.Is(err, apperror.ErrSpendingAlertNotFound) {
			return httpresponse.Error(c, fiber.StatusNotFound, "no active alert with that id")
		}
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to cancel alert")
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{"id": id})
}
