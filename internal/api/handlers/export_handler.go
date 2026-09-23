package handlers

import (
	"github.com/gofiber/fiber/v2"

	"aura-saku/pkg/httpresponse"
)

type ExportHandler struct{}

func NewExportHandler() *ExportHandler {
	return &ExportHandler{}
}

// Export will stream a CSV/Excel export of a user's transactions.
//
// TODO: implement CSV via encoding/csv and Excel via an xlsx library,
// streaming the same query used by TransactionHandler.List without pagination.
func (h *ExportHandler) Export(c *fiber.Ctx) error {
	format := c.Query("format", "csv")
	if format != "csv" && format != "xlsx" {
		return httpresponse.Error(c, fiber.StatusBadRequest, "format must be csv or xlsx")
	}
	return httpresponse.Error(c, fiber.StatusNotImplemented, "export is not wired up yet")
}
