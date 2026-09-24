package telegram

import (
	"context"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"

	"aura-saku/internal/idempotency"
	"aura-saku/internal/logger"
	"aura-saku/internal/service/bot"
)

// NewWebhookHandler returns a Fiber handler for the Telegram webhook route.
// It verifies the webhook secret, parses the payload, checks it hasn't
// already been processed (Telegram redelivers an update if it doesn't get a
// timely 200 OK), and immediately ACKs 200 OK before dispatching the actual
// handling to a goroutine — Telegram's webhook contract rewards a fast
// response, so all AI/DB work happens async while the HTTP handler returns
// right away.
//
// TODO(production hardening): replace the raw `go` dispatch with a bounded
// worker pool / job queue before real traffic, to avoid unbounded goroutine
// growth under burst load.
func NewWebhookHandler(client *Client, botSvc *bot.Service, secretToken string, redisClient *redis.Client) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if secretToken != "" && c.Get("X-Telegram-Bot-Api-Secret-Token") != secretToken {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		msg, err := client.ParseWebhookPayload(c.Body())
		if err != nil {
			logger.Error(logger.TelegramParseFailed, "telegram webhook parse payload", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		isNew, err := idempotency.MarkSeen(c.Context(), redisClient, "telegram-update", strconv.FormatInt(msg.RawUpdateID, 10))
		if err != nil {
			// Fail open: a rare duplicate on a Redis hiccup is a much smaller
			// problem than dropping legitimate updates during an outage.
			logger.Error(logger.RedisOpFailed, "webhook idempotency check, failing open", err)
		} else if !isNew {
			// Telegram redelivered an update we've already processed (or are
			// still processing) — ack without reprocessing.
			return c.SendStatus(fiber.StatusOK)
		}

		go botSvc.HandleUpdate(context.Background(), msg)

		return c.SendStatus(fiber.StatusOK)
	}
}
