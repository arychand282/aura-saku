package openai

import (
	"context"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
)

// Client is a placeholder OpenAI implementation. It satisfies ai.AIClient
// so it's selectable via AI_PROVIDER=openai and wired in factory.go today,
// ready for a real implementation (Chat Completions + Whisper for voice)
// later without touching any caller.
type Client struct {
	apiKey string
	model  string
}

func New(apiKey, model string) *Client {
	return &Client{apiKey: apiKey, model: model}
}

var _ ai.AIClient = (*Client)(nil)

func (c *Client) ProviderName() string { return "openai" }

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrNotImplemented
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrNotImplemented
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrNotImplemented
}
