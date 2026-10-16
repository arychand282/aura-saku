package handlers

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/service/account"
	"aura-saku/pkg/httpresponse"
)

type AccountHandler struct {
	db *gorm.DB
}

func NewAccountHandler(db *gorm.DB) *AccountHandler {
	return &AccountHandler{db: db}
}

type accountRow struct {
	ID        uint    `json:"id"`
	Name      string  `json:"name"`
	IsDefault bool    `json:"is_default"`
	Balance   float64 `json:"balance"`
}

// List returns every one of the user's active accounts (see
// account.ListWithBalances for what "active" means — an account with no
// active transaction posted to it is never listed) with each one's own
// balance, plus the total across all of them.
func (h *AccountHandler) List(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	balances, err := account.ListWithBalances(c.Context(), h.db, userID)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to list accounts")
	}

	rows := make([]accountRow, 0, len(balances))
	var total float64
	for _, b := range balances {
		rows = append(rows, accountRow{ID: b.Account.ID, Name: b.Account.Name, IsDefault: b.Account.IsDefault, Balance: b.Balance})
		total += b.Balance
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"accounts": rows,
		"total":    total,
	})
}

type setDefaultAccountRequest struct {
	AccountName string `json:"account_name"`
}

// SetDefault re-designates which account untagged transactions fall back
// to from now on — see account.SetDefault for exactly what this does and
// doesn't change (only future transactions, never past ones).
func (h *AccountHandler) SetDefault(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	var req setDefaultAccountRequest
	if err := c.BodyParser(&req); err != nil || req.AccountName == "" {
		return httpresponse.Error(c, fiber.StatusBadRequest, "account_name is required")
	}

	acc, err := account.SetDefault(c.Context(), h.db, userID, req.AccountName)
	if err != nil {
		return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to set default account")
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{
		"id": acc.ID, "name": acc.Name, "is_default": acc.IsDefault,
	})
}

type renameAccountRequest struct {
	OldName string `json:"old_name"`
	NewName string `json:"new_name"`
}

// Rename changes an existing account's display name — see account.Rename
// for the exact collision/not-found semantics.
func (h *AccountHandler) Rename(c *fiber.Ctx) error {
	userID := effectiveUserID(c)

	var req renameAccountRequest
	if err := c.BodyParser(&req); err != nil || req.OldName == "" || req.NewName == "" {
		return httpresponse.Error(c, fiber.StatusBadRequest, "old_name and new_name are required")
	}

	acc, err := account.Rename(c.Context(), h.db, userID, req.OldName, req.NewName)
	if err != nil {
		switch {
		case errors.Is(err, apperror.ErrAccountNotFound):
			return httpresponse.Error(c, fiber.StatusNotFound, "no account with that name")
		case errors.Is(err, apperror.ErrAccountNameTaken):
			return httpresponse.Error(c, fiber.StatusConflict, "an account with that name already exists")
		default:
			return httpresponse.Error(c, fiber.StatusInternalServerError, "failed to rename account")
		}
	}

	return httpresponse.Success(c, fiber.StatusOK, fiber.Map{"id": acc.ID, "name": acc.Name})
}
