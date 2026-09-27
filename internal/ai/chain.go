package ai

import "context"

// LastTransaction is what a get_last_transaction-style tool hands back to
// a chained-tool-calling provider (see gemini.Client.ParseCorrectionChained,
// the only implementation today). Kept in this package rather than gemini
// so bot.Service can supply a LastTransactionLookup backed by the real
// transaction.Service.MostRecent without importing a concrete provider
// package — the same interface-only boundary every other AIClient method
// already respects.
type LastTransaction struct {
	Found        bool
	Type         string
	Amount       float64
	Currency     string
	CategoryName string
	Bucket       string
	Description  string
	OccurredAt   string // YYYY-MM-DD
}

// LastTransactionLookup executes the real get_last_transaction tool. Only
// called when (and only when) the model actually asks for it — a
// correction message that already restates every field never triggers it.
type LastTransactionLookup func(ctx context.Context) (*LastTransaction, error)

// ChainStep records one round of a chained call — which tool the model
// asked for, with what arguments, and (for get_last_transaction) what real
// result it got back.
type ChainStep struct {
	ToolCalled string
	Args       map[string]interface{}
	Result     *LastTransaction
}

// LastTransactionToMap converts a LastTransaction into the plain
// map[string]interface{} shape every provider feeds back to the model as
// its tool result (Gemini's functionResponse, OpenAI's tool message
// content, Anthropic's tool_result block) — shared so this mapping only
// exists once instead of three times.
func LastTransactionToMap(lt *LastTransaction) map[string]interface{} {
	if lt == nil || !lt.Found {
		return map[string]interface{}{"found": false}
	}
	return map[string]interface{}{
		"found":         true,
		"type":          lt.Type,
		"amount":        lt.Amount,
		"currency":      lt.Currency,
		"category_name": lt.CategoryName,
		"bucket":        lt.Bucket,
		"description":   lt.Description,
		"occurred_at":   lt.OccurredAt,
	}
}

// ChainedCorrector is an optional capability some AIClient implementations
// support: multi-step tool calling for corrections that don't restate
// every field of the transaction being corrected. Providers without a
// tool-calling loop (openai, anthropic today) simply don't satisfy this
// interface — callers should type-assert (`aiClient.(ChainedCorrector)`)
// and fall back to ParseTransactionText when they don't, exactly as
// bot.Service does.
type ChainedCorrector interface {
	// Same one-or-more-results contract as AIClient's parsing methods — the
	// terminal round can still yield several ParsedTransaction results
	// (e.g. a message with both a correction and, unusually, a separate new
	// transaction), not just corrections.
	ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, lookup LastTransactionLookup) ([]*ParsedTransaction, *UsageMetadata, []ChainStep, error)
}
