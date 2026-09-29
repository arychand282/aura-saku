package ai

import "context"

// RecentTransaction is one of the user's recent transactions, handed back by
// a get_recent_transactions-style tool (see gemini/openai/anthropic
// chain.go) so a chained-tool-calling provider can identify EXACTLY which
// past transaction a correction message refers to instead of always
// assuming it's the newest one. ID is the real database primary key, echoed
// back unchanged by the model as correct_transaction's
// target_transaction_id so bot.Service can look the row up directly rather
// than falling back to "most recent".
type RecentTransaction struct {
	ID           uint
	Type         string
	Amount       float64
	Currency     string
	CategoryName string
	Bucket       string
	Description  string
	OccurredAt   string // YYYY-MM-DD
}

// RecentTransactionsLookup executes the real get_recent_transactions tool.
// Only called when (and only when) the model actually asks for it — a
// correction message that's unambiguously about the single most recent
// transaction and already restates every field never triggers it. An empty
// slice (not an error) means the user has no transactions yet.
type RecentTransactionsLookup func(ctx context.Context) ([]*RecentTransaction, error)

// ChainStep records one round of a chained call — which tool the model
// asked for, with what arguments, and (for get_recent_transactions) what
// real candidates it got back.
type ChainStep struct {
	ToolCalled string
	Args       map[string]interface{}
	Results    []*RecentTransaction
}

// RecentTransactionsToMap converts a []*RecentTransaction into the plain
// map[string]interface{} shape every provider feeds back to the model as
// its tool result (Gemini's functionResponse, OpenAI's tool message
// content, Anthropic's tool_result block) — shared so this mapping only
// exists once instead of three times. Each entry keeps its real id so the
// model can echo the matching one back as target_transaction_id.
func RecentTransactionsToMap(txs []*RecentTransaction) map[string]interface{} {
	list := make([]map[string]interface{}, 0, len(txs))
	for _, t := range txs {
		list = append(list, map[string]interface{}{
			"id":            t.ID,
			"type":          t.Type,
			"amount":        t.Amount,
			"currency":      t.Currency,
			"category_name": t.CategoryName,
			"bucket":        t.Bucket,
			"description":   t.Description,
			"occurred_at":   t.OccurredAt,
		})
	}
	return map[string]interface{}{"transactions": list}
}

// ChainedCorrector is an optional capability an AIClient implementation may
// support: multi-step tool calling for corrections that don't restate every
// field of the transaction being corrected, or that target something other
// than the single most recent transaction. All three providers (gemini,
// openai, anthropic) implement this today, but callers should still
// type-assert (`aiClient.(ChainedCorrector)`) and fall back to
// ParseTransactionText when they don't, exactly as bot.Service does — this
// stays an optional capability, not folded into AIClient itself, since a
// future provider without multi-round tool calling could still satisfy
// AIClient alone.
type ChainedCorrector interface {
	// Same one-or-more-results contract as AIClient's parsing methods — the
	// terminal round can still yield several ParsedTransaction results (e.g.
	// a message with both a correction and, unusually, a separate new
	// transaction), not just corrections. Same history parameter and
	// zero-value-means-no-history contract as AIClient's methods too.
	ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, lookup RecentTransactionsLookup, history ConversationContext) ([]*ParsedTransaction, *UsageMetadata, []ChainStep, error)
}
