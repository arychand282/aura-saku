package telegram

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/logger"
	"aura-saku/internal/service/bot"
)

// NewWebhookHandler returns a Fiber handler for the Telegram webhook route.
// It verifies the webhook secret, parses the payload, and immediately ACKs
// 200 OK before dispatching the actual handling to a goroutine — Telegram's
// webhook contract rewards a fast response, so all AI/DB work happens async
// while the HTTP handler returns right away.
//
// TODO(production hardening): replace the raw `go` dispatch with a bounded
// worker pool / job queue before real traffic, to avoid unbounded goroutine
// growth under burst load.
func NewWebhookHandler(client *Client, botSvc *bot.Service, secretToken string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if secretToken != "" && c.Get("X-Telegram-Bot-Api-Secret-Token") != secretToken {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		msg, err := client.ParseWebhookPayload(c.Body())
		if err != nil {
			logger.Error(logger.TelegramParseFailed, "telegram webhook parse payload", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		go botSvc.HandleUpdate(context.Background(), msg)

		return c.SendStatus(fiber.StatusOK)
	}
}
