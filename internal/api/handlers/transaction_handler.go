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
func (h *TransactionHandler) List(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	page, _ := strconv.Atoi(c.Query("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.Query("limit", "20"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	rows, err := fetchTransactionRows(c.Context(), h.db, userID, limit, (page-1)*limit)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to fetch transactions")
	}

	var totalCount int64
	if err := h.db.Model(&models.Transaction{}).Where("user_id = ?", userID).Count(&totalCount).Error; err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to count transactions")
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"transactions": rows,
		"page":         page,
		"limit":        limit,
		"total_count":  totalCount,
	})
}
