package handlers

import (
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/models"
	"aura-saku/internal/service/debt"
	"aura-saku/pkg/httpresponse"
)

type DebtHandler struct {
	db *gorm.DB
}

func NewDebtHandler(db *gorm.DB) *DebtHandler {
	return &DebtHandler{db: db}
}

type debtRow struct {
	ID           uint    `json:"id"`
	Counterparty string  `json:"counterparty"`
	Remaining    float64 `json:"remaining"`
}

// List returns every one of the user's open debts, split by direction —
// see debt.ListOpen for what "open" means (a fully-repaid debt is excluded
// automatically, not just hidden). Creating/repaying a loan stays
// Telegram-only for now — this endpoint is read-only by design.
func (h *DebtHandler) List(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	open, err := debt.ListOpen(c.Context(), h.db, userID)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to list debts")
	}

	owedToYou := make([]debtRow, 0, len(open))
	youOwe := make([]debtRow, 0, len(open))
	for _, d := range open {
		row := debtRow{ID: d.Debt.ID, Counterparty: d.Debt.Counterparty, Remaining: d.Remaining}
		if d.Debt.Direction == models.DebtOwedToMe {
			owedToYou = append(owedToYou, row)
		} else {
			youOwe = append(youOwe, row)
		}
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"owed_to_you": owedToYou,
		"you_owe":     youOwe,
	})
}
