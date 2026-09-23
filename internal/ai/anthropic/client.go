package anthropic

import (
	"context"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
)

// Client is a placeholder Anthropic (Claude) implementation. It satisfies
// ai.AIClient so it's selectable via AI_PROVIDER=anthropic and wired in
// factory.go today. Note: Claude has no native audio transcription, so a
// real implementation of TranscribeAndParseAudio would need a separate STT
// step (e.g. a dedicated transcription service) before calling into Claude
// for parsing.
type Client struct {
	apiKey string
	model  string
}

func New(apiKey, model string) *Client {
	return &Client{apiKey: apiKey, model: model}
}

var _ ai.AIClient = (*Client)(nil)

func (c *Client) ProviderName() string { return "anthropic" }

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrNotImplemented
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrNotImplemented
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrNotImplemented
}
