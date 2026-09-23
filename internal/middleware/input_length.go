package middleware

import (
	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/apperror"
	"aura-saku/internal/messaging"
)

const IncomingMessageLocalsKey = "incomingMessage"

// LimitInputLength is the first middleware in the webhook chain: it parses
// the raw payload once via the injected Messenger (so RateLimiter/DailyQuota
// downstream can reuse the parsed message from c.Locals instead of
// re-parsing JSON three times), then rejects text messages longer than
// maxChars BEFORE any AI API call is ever reached. Voice messages have no
// text to check and pass through this gate.
func LimitInputLength(messenger messaging.Messenger, maxChars int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		msg, err := messenger.ParseWebhookPayload(c.Body())
		if err != nil {
			return c.SendStatus(fiber.StatusBadRequest)
		}
		c.Locals(IncomingMessageLocalsKey, msg)

		if !msg.IsVoice && len(msg.Text) > maxChars {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
				"error": apperror.ErrInputTooLong.Error(),
			})
		}

		return c.Next()
	}
}
