package handlers

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/logger"
	"aura-saku/internal/service/bot"
)

// NewMidtransWebhookHandler returns a Fiber handler for Midtrans's payment
// notification callback. Always acks 200 OK regardless of internal outcome
// — Midtrans only cares that the endpoint received the callback, and
// retry-storms an endpoint that keeps returning non-2xx, so a failure here
// is logged rather than surfaced as an HTTP error. Processing happens in a
// goroutine (same "ack fast, work async" shape as the Telegram webhook
// handler) since bot.Service.HandleMidtransNotification's signature
// verification, a Midtrans CheckTransaction round trip, and a DB write are
// more latency than a webhook caller should be kept waiting on.
func NewMidtransWebhookHandler(botSvc *bot.Service) fiber.Handler {
	return func(c *fiber.Ctx) error {
		body := append([]byte(nil), c.Body()...)

		go func() {
			if err := botSvc.HandleMidtransNotification(context.Background(), body); err != nil {
				logger.Error(logger.SvcPaymentFailed, "handle midtrans notification", err)
			}
		}()

		return c.SendStatus(fiber.StatusOK)
	}
}
