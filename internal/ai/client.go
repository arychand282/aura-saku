package ai

import (
	"context"
)

// Intent values for ParsedTransaction.Intent. Empty string is treated the
// same as IntentRecord, so every prompt/response that predates the "query"
// intent (and any provider test fixture) keeps working unchanged.
const (
	IntentRecord  = "record"
	IntentQuery   = "query"
	IntentBalance = "balance"
	IntentCorrect = "correct"
)

// Valid values for ParsedTransaction.QueryPeriod — matches the period
// vocabulary already used by ReportHandler/report.Service and the dashboard's
// PeriodSelector, so a query-intent parse plugs straight into that existing
// period logic.
const (
	PeriodDaily   = "daily"
	PeriodWeekly  = "weekly"
	PeriodMonthly = "monthly"
	PeriodYearly  = "yearly"
)

// ParsedTransaction is the structured-output contract every provider must
// populate from free-form text or transcribed audio. See
// internal/ai/prompt/tool_schema.go for the declared tool/parameter shapes
// the model is offered, and ParsedTransactionFromToolCall (toolcall.go) for
// how a tool call maps back onto this struct.
type ParsedTransaction struct {
	IsAmbiguous           bool   `json:"is_ambiguous"`
	ClarificationQuestion string `json:"clarification_question,omitempty"`

	// Intent distinguishes what this message is asking for. Empty/IntentRecord
	// preserves the original record-only behavior, populating Type/Amount/...
	// below. IntentQuery instead uses QueryPeriod/QueryCategory and leaves
	// Type/Amount/CategoryName zero-valued. IntentBalance needs nothing else
	// at all — a balance question has no period or category. IntentCorrect
	// populates the exact same fields as IntentRecord (it's "here's the
	// corrected version of my last message," not a diff) — the caller
	// (bot.Service) undoes the previous transaction and creates this one in
	// its place instead of just creating a new one alongside it.
	Intent string `json:"intent,omitempty"`

	Type           string  `json:"type"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"currency"`
	CategoryName   string  `json:"category_name"`
	Description    string  `json:"description"`
	OccurredAtHint string  `json:"occurred_at_hint,omitempty"`

	// Bucket is the AI's per-transaction Needs/Wants/Savings classification
	// from the actual message context (e.g. "replacing broken shoes" reads
	// as needs even though the category is "shopping") — not a static
	// category-name lookup. Only meaningful for IntentRecord. Empty or an
	// unrecognized value means "no confident classification"; the caller
	// (transaction.Service.CreateFromParsed) falls back to the category's
	// own default bucket rather than rejecting the whole parse over this —
	// same defensive philosophy as OccurredAtHint's fallback below, so
	// ValidateParsedTransaction doesn't need a new failure mode for it.
	Bucket string `json:"bucket,omitempty"`

	// QueryPeriod/QueryCategory are only populated when Intent==IntentQuery.
	// QueryPeriod is always one of the Period* constants above — the prompt
	// instructs the model to default to "monthly" rather than leaving it
	// empty, since the bot has no way to ask a clarifying follow-up and
	// actually continue this same query (see internal/service/bot).
	// QueryCategory is English free text, same convention as CategoryName;
	// empty means "no category filter, total everything".
	QueryPeriod   string `json:"query_period,omitempty"`
	QueryCategory string `json:"query_category,omitempty"`
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
	// ParseTransactionText sends raw user text and expects back one or more
	// ParsedTransaction results — almost always exactly one, but a message
	// listing several distinct transactions (e.g. an itemized "bensin 300k,
	// makan siang 300k, makan malam 100k") yields one entry per transaction,
	// in the order mentioned, from a single AI call (see
	// internal/ai/prompt/tool_schema.go: record_transaction is the only tool
	// a provider may call more than once per turn). Always non-empty on a
	// nil error — even the ambiguous case is a single-element slice.
	// preferredLanguage ("id"/"en") controls what language
	// ClarificationQuestion comes back in — category_name is always English
	// regardless (see internal/ai/prompt), since it feeds internal budget
	// categorization.
	// history carries prior conversation turns (see ConversationContext) so
	// a follow-up message can be understood in context instead of parsed in
	// isolation — e.g. "totalnya 150rb" only makes sense as a food expense
	// if the model can see the previous "abis makan di restoran mahal
	// tadi." Pass the zero value ConversationContext{} for "no history,"
	// which must behave identically to how this method worked before
	// history existed.
	ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string, history ConversationContext) ([]*ParsedTransaction, *UsageMetadata, error)

	// TranscribeAndParseAudio handles voice notes. Providers with native
	// audio support (Gemini) transcribe+parse in one round trip; providers
	// without it would transcribe then internally call ParseTransactionText.
	// Same one-or-more-results contract as ParseTransactionText, and the
	// same history parameter.
	TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string, history ConversationContext) ([]*ParsedTransaction, *UsageMetadata, error)

	// AnswerClarification re-parses given the original ambiguous context
	// plus the user's clarifying reply. Same one-or-more-results contract as
	// ParseTransactionText, though a clarification reply is answering one
	// specific ambiguous transaction and so realistically always yields
	// exactly one result.
	AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string, history ConversationContext) ([]*ParsedTransaction, *UsageMetadata, error)

	// Summarize condenses priorSummary (empty if none yet) plus turns into
	// an updated summary, in preferredLanguage, standing in for everything
	// older than the "recent" window a caller keeps as raw ConversationTurns
	// — see internal/service/conversation.Service.MaybeSummarize. A plain
	// text completion, not a tool call: there's nothing to extract here,
	// just condense.
	Summarize(ctx context.Context, priorSummary string, turns []ConversationTurn, preferredLanguage string) (string, *UsageMetadata, error)

	ProviderName() string
}

// ValidateParsedTransaction is the shared schema-shape check every
// provider's response must pass before it's trusted. Query-intent responses
// only need a real period; balance-intent responses need nothing at all
// (the record-intent Type/Amount checks below don't apply to either — those
// fields are left zero-valued by the prompt). IntentCorrect isn't special-
// cased here since it populates the exact same fields as IntentRecord, so
// it naturally falls through to the same Type/Amount validation below.
func ValidateParsedTransaction(pt *ParsedTransaction) bool {
	switch pt.Intent {
	case IntentQuery:
		switch pt.QueryPeriod {
		case PeriodDaily, PeriodWeekly, PeriodMonthly, PeriodYearly:
			return true
		default:
			return false
		}
	case IntentBalance:
		return true
	}

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
