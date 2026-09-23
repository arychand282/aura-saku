package ai

import "context"

// ParsedTransaction is the structured-output contract every provider must
// populate from free-form text or transcribed audio. See
// internal/ai/prompt/transaction_prompt.go for the exact JSON shape the
// model is instructed to return.
type ParsedTransaction struct {
	IsAmbiguous           bool   `json:"is_ambiguous"`
	ClarificationQuestion string `json:"clarification_question,omitempty"`

	Type           string  `json:"type"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"currency"`
	CategoryName   string  `json:"category_name"`
	Description    string  `json:"description"`
	OccurredAtHint string  `json:"occurred_at_hint,omitempty"`
}

type UsageMetadata struct {
	Model            string
	PromptTokens     int
	CompletionTokens int
	LatencyMs        int
}

// AIClient is the pluggable contract every LLM provider must satisfy.
// Concrete implementations live in internal/ai/{gemini,openai,anthropic}.
// Swapping providers is a config-only change (see factory.go) — no caller
// outside this package ever imports a vendor SDK directly.
type AIClient interface {
	// ParseTransactionText sends raw user text and expects back structured
	// JSON conforming to ParsedTransaction.
	ParseTransactionText(ctx context.Context, userText string, defaultCurrency string) (*ParsedTransaction, *UsageMetadata, error)

	// TranscribeAndParseAudio handles voice notes. Providers with native
	// audio support (Gemini) transcribe+parse in one round trip; providers
	// without it would transcribe then internally call ParseTransactionText.
	TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string) (*ParsedTransaction, *UsageMetadata, error)

	// AnswerClarification re-parses given the original ambiguous context
	// plus the user's clarifying reply.
	AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string) (*ParsedTransaction, *UsageMetadata, error)

	ProviderName() string
}
