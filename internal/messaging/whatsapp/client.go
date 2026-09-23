package whatsapp

import (
	"context"

	"aura-saku/internal/apperror"
	"aura-saku/internal/messaging"
)

// Client is a placeholder for the future WhatsApp Business API integration.
// It satisfies messaging.Messenger so bot.Service can be pointed at it with
// zero core-logic changes once implemented — mirroring how the Telegram
// client is wired in cmd/server/main.go today.
type Client struct{}

func New() *Client { return &Client{} }

var _ messaging.Messenger = (*Client)(nil)

func (c *Client) PlatformName() string { return "whatsapp" }

func (c *Client) ParseWebhookPayload(body []byte) (*messaging.IncomingMessage, error) {
	return nil, apperror.ErrNotImplemented
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	return apperror.ErrNotImplemented
}

func (c *Client) SendTypingIndicator(ctx context.Context, chatID int64) error {
	return apperror.ErrNotImplemented
}

func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
	return nil, "", apperror.ErrNotImplemented
}
