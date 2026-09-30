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

// RecurringCandidate is one of the user's active recurring transactions or
// bill reminders, handed back by a get_recurring_transactions-style tool so
// a chained-tool-calling provider can identify EXACTLY which one a
// cancellation message refers to (e.g. "batalin netflix") instead of
// guessing. The list is typically short (a handful per user), but that's
// exactly why a lookup — not a blind guess — is cheap and worth doing. ID
// is the real database primary key, echoed back unchanged as
// delete_recurring_transaction's target_recurring_transaction_id.
type RecurringCandidate struct {
	ID           uint
	Type         string
	Amount       float64
	Currency     string
	CategoryName string
	Description  string
	DayOfMonth   int
	ReminderOnly bool
}

// RecurringTransactionsLookup executes the real get_recurring_transactions
// tool. An empty slice (not an error) means the user has no active
// recurring transactions or bill reminders.
type RecurringTransactionsLookup func(ctx context.Context) ([]*RecurringCandidate, error)

// RecurringTransactionsToMap mirrors RecentTransactionsToMap for
// RecurringCandidate — shared so this mapping exists once instead of three
// times across the providers.
func RecurringTransactionsToMap(rows []*RecurringCandidate) map[string]interface{} {
	list := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		list = append(list, map[string]interface{}{
			"id":            r.ID,
			"type":          r.Type,
			"amount":        r.Amount,
			"currency":      r.Currency,
			"category_name": r.CategoryName,
			"description":   r.Description,
			"day_of_month":  r.DayOfMonth,
			"reminder_only": r.ReminderOnly,
		})
	}
	return map[string]interface{}{"recurring_transactions": list}
}

// ChainStep records one round of a chained call — which tool the model
// asked for, with what arguments, and what real candidates it got back
// (Results for get_recent_transactions, RecurringResults for
// get_recurring_transactions — a round only ever populates one of the two,
// since a round is either kind of lookup or the terminal round).
type ChainStep struct {
	ToolCalled       string
	Args             map[string]interface{}
	Results          []*RecentTransaction
	RecurringResults []*RecurringCandidate
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
// support: multi-step tool calling for corrections/deletions that don't
// restate every field or don't target the single most recent
// transaction/recurring schedule. All three providers (gemini, openai,
// anthropic) implement this today, but callers should still type-assert
// (`aiClient.(ChainedCorrector)`) and fall back to ParseTransactionText
// when they don't, exactly as bot.Service does — this stays an optional
// capability, not folded into AIClient itself, since a future provider
// without multi-round tool calling could still satisfy AIClient alone.
type ChainedCorrector interface {
	// Same one-or-more-results contract as AIClient's parsing methods — the
	// terminal round can still yield several ParsedTransaction results (e.g.
	// a message with both a correction and, unusually, a separate new
	// transaction), not just corrections. Same history parameter and
	// zero-value-means-no-history contract as AIClient's methods too.
	// txLookup backs get_recent_transactions (for correcting/deleting a past
	// transaction); recurringLookup backs get_recurring_transactions (for
	// canceling a recurring transaction/bill reminder) — a single round only
	// ever calls one lookup tool, detected independently, since the system
	// instruction caps every non-record tool at once per message.
	ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, txLookup RecentTransactionsLookup, recurringLookup RecurringTransactionsLookup, history ConversationContext) ([]*ParsedTransaction, *UsageMetadata, []ChainStep, error)
}
