package ai

import (
	"context"
	"regexp"
	"strings"
)

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
	// JSON conforming to ParsedTransaction. preferredLanguage ("id"/"en")
	// controls what language ClarificationQuestion comes back in —
	// category_name is always English regardless (see internal/ai/prompt),
	// since it feeds internal budget categorization.
	ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string) (*ParsedTransaction, *UsageMetadata, error)

	// TranscribeAndParseAudio handles voice notes. Providers with native
	// audio support (Gemini) transcribe+parse in one round trip; providers
	// without it would transcribe then internally call ParseTransactionText.
	TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string) (*ParsedTransaction, *UsageMetadata, error)

	// AnswerClarification re-parses given the original ambiguous context
	// plus the user's clarifying reply.
	AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string) (*ParsedTransaction, *UsageMetadata, error)

	ProviderName() string
}

var jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

// StripJSONFences removes a markdown code fence LLMs commonly wrap JSON
// output in despite being told not to, shared by every provider's response
// parser so this quirk is only handled in one place.
func StripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if m := jsonFenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

// ValidateParsedTransaction is the shared schema-shape check every
// provider's response must pass before it's trusted: a real, known
// transaction type, and a positive amount unless the model flagged the
// input as ambiguous.
func ValidateParsedTransaction(pt *ParsedTransaction) bool {
	switch pt.Type {
	case "expense", "income", "investment", "transfer", "reward":
	default:
		return false
	}
	if !pt.IsAmbiguous && pt.Amount <= 0 {
		return false
	}
	return true
}
