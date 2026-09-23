package handlers

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/models"
	"aura-saku/pkg/httpresponse"
)

type TransactionHandler struct {
	db *gorm.DB
}

func NewTransactionHandler(db *gorm.DB) *TransactionHandler {
	return &TransactionHandler{db: db}
}

// List returns a paginated list of a user's transactions.
//
// TODO(auth): once the dashboard has real session auth, derive user_id from
// the authenticated session instead of trusting a query param.
func (h *TransactionHandler) List(c *fiber.Ctx) error {
	userID, err := strconv.ParseUint(c.Query("user_id"), 10, 64)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusBadRequest, "user_id query param is required")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	var transactions []models.Transaction
	result := h.db.
		Where("user_id = ?", userID).
		Order("occurred_at DESC").
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&transactions)
	if result.Error != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to fetch transactions")
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"transactions": transactions,
		"page":         page,
		"limit":        limit,
	})
}
