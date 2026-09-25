package middleware_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/apperror"
	"aura-saku/internal/messaging"
	"aura-saku/internal/middleware"
)

// fakeMessenger returns a fixed parse result/error, letting tests drive
// ParseIncomingMessage's branching without a real Telegram/WhatsApp client.
type fakeMessenger struct {
	msg *messaging.IncomingMessage
	err error
}

func (f *fakeMessenger) ParseWebhookPayload(body []byte) (*messaging.IncomingMessage, error) {
	return f.msg, f.err
}
func (f *fakeMessenger) SendMessage(ctx context.Context, chatID int64, text string) error { return nil }
func (f *fakeMessenger) SendTypingIndicator(ctx context.Context, chatID int64) error      { return nil }
func (f *fakeMessenger) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	return nil, "", nil
}
func (f *fakeMessenger) PlatformName() string { return "fake" }

func newTestApp(messenger messaging.Messenger) *fiber.App {
	app := fiber.New()
	app.Post("/webhook", middleware.ParseIncomingMessage(messenger), func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})
	return app
}

// TestParseIncomingMessage_UnhandledUpdateType covers the actual bug this
// guards against: this middleware runs before telegram.NewWebhookHandler in
// the router chain (see internal/api/router.go), so it must independently
// handle apperror.ErrUnhandledUpdateType the same way — otherwise it 400s
// an edited message / callback query / etc. before the handler's own
// (correct) handling of that case is ever reached, and Telegram retries the
// same update forever with exponential backoff.
func TestParseIncomingMessage_UnhandledUpdateType(t *testing.T) {
	app := newTestApp(&fakeMessenger{err: apperror.ErrUnhandledUpdateType})

	req := httptest.NewRequest(fiber.MethodPost, "/webhook", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("status = %d, want %d (ack, not a failure)", resp.StatusCode, fiber.StatusOK)
	}
}

func TestParseIncomingMessage_MalformedPayloadStill400s(t *testing.T) {
	app := newTestApp(&fakeMessenger{err: fiber.NewError(fiber.StatusBadRequest, "boom")})

	req := httptest.NewRequest(fiber.MethodPost, "/webhook", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
}

func TestParseIncomingMessage_ValidPayloadPassesThrough(t *testing.T) {
	app := newTestApp(&fakeMessenger{msg: &messaging.IncomingMessage{ChatID: 1, Text: "hi"}})

	req := httptest.NewRequest(fiber.MethodPost, "/webhook", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Errorf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}
}
