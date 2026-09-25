package middleware

import (
	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/messaging"
)

const IncomingMessageLocalsKey = "incomingMessage"

// ParseIncomingMessage is the first middleware in the webhook chain: it
// parses the raw payload once via the injected Messenger and stores it in
// c.Locals, so RateLimiter downstream can reuse the parsed message instead
// of re-parsing the same JSON body again.
//
// The too-long-input check that used to live here was moved into
// bot.Service (see enforceInputLength) so a rejected message gets an actual
// reply in the chat, instead of a silent HTTP-level rejection the user
// never sees — the same reasoning that already moved daily-quota
// enforcement out of middleware.
func ParseIncomingMessage(messenger messaging.Messenger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		msg, err := messenger.ParseWebhookPayload(c.Body())
		if err != nil {
			return c.SendStatus(fiber.StatusBadRequest)
		}
		c.Locals(IncomingMessageLocalsKey, msg)
		return c.Next()
	}
}
