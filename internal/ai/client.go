package ai

import (
	"context"
	"strings"
)

// Intent values for ParsedTransaction.Intent. Empty string is treated the
// same as IntentRecord, so every prompt/response that predates the "query"
// intent (and any provider test fixture) keeps working unchanged.
const (
	IntentRecord             = "record"
	IntentQuery              = "query"
	IntentBalance            = "balance"
	IntentCorrect            = "correct"
	IntentDelete             = "delete"
	IntentRecurringCreate    = "recurring_create"
	IntentRecurringDelete    = "recurring_delete"
	IntentSavingsGoalCreate  = "savings_goal_create"
	IntentSubscribe          = "subscribe"
	IntentSubscriptionStatus = "subscription_status"
	IntentBudgetAllocation   = "budget_allocation"
	IntentBudgetSet          = "budget_set"
	IntentSurplus            = "surplus"
	IntentAccountTransfer    = "account_transfer"
	IntentDebtLend           = "debt_lend"
	IntentDebtRepay          = "debt_repay"
	IntentDebtQuery          = "debt_query"
	IntentSetSpendingAlert   = "set_spending_alert"
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

// QueryModeList is ParsedTransaction.QueryMode's non-default value — see
// that field's own doc comment. There's no QueryModeTotal constant since
// empty (and any unrecognized value) already means "total", the original
// behavior before QueryMode existed.
const QueryModeList = "list"

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
	// IntentDelete needs nothing but TargetTransactionID (below) — the
	// caller just removes that transaction, nothing is created. IntentRecurringCreate
	// populates the same Type/Amount/Currency/CategoryName/Description/Bucket
	// fields as IntentRecord, plus DayOfMonth below — the caller
	// (recurring.Service.Create) schedules future postings instead of
	// creating a Transaction right away.
	Intent string `json:"intent,omitempty"`

	Type           string  `json:"type"`
	Amount         float64 `json:"amount"`
	Currency       string  `json:"currency"`
	CategoryName   string  `json:"category_name"`
	Description    string  `json:"description"`
	OccurredAtHint string  `json:"occurred_at_hint,omitempty"`

	// AccountName is which of the user's own tracked accounts/wallets this
	// applies to, when the message explicitly names one (e.g. "beli kopi
	// 20rb pake OVO" -> "OVO") — empty means "use their default account."
	// Meaningful for IntentRecord/IntentCorrect/IntentRecurringCreate (goes
	// through transactionFromArgs, same as CategoryName) and also for
	// IntentBalance, where it narrows a balance question to one specific
	// account instead of the total across all of them (see
	// bot.Service.handleBalanceQuery).
	AccountName string `json:"account_name,omitempty"`

	// FromAccountName/ToAccountName are only meaningful for
	// IntentAccountTransfer (move_money) — moving money between two of the
	// user's OWN accounts, as opposed to a plain Type=transfer record
	// (money sent to someone else, which stays on a single account exactly
	// as before this field existed). See transaction.Service.CreateTransfer.
	FromAccountName string `json:"from_account,omitempty"`
	ToAccountName   string `json:"to_account,omitempty"`

	// DebtCounterparty/DebtDirection are only meaningful for
	// IntentDebtLend/IntentDebtRepay/IntentDebtQuery (record_loan/
	// record_repayment/check_debts) — DebtCounterparty is the person's name;
	// DebtDirection is the raw "lent"/"borrowed" (IntentDebtLend) or
	// "received"/"made" (IntentDebtRepay) value, left empty for
	// IntentDebtQuery. Amount/Currency/Description/OccurredAtHint/
	// AccountName above are reused for the loan/repayment amount and which
	// of the user's own accounts the cash moved from/into — see
	// transaction.Service.CreateLoan/CreateRepayment.
	DebtCounterparty string `json:"counterparty,omitempty"`
	DebtDirection    string `json:"direction,omitempty"`

	// AlertScope is only meaningful for IntentSetSpendingAlert
	// (set_spending_alert) — "category"/"bucket"/"total". Everything else a
	// spending alert needs is reused from fields already above: CategoryName
	// for a category-scoped alert, Bucket for a bucket-scoped one, Amount as
	// the threshold amount, and QueryPeriod (below) as the reset period —
	// the same "reuse existing fields across intents" convention
	// IntentSavingsGoalCreate already uses for Amount/Currency.
	AlertScope string `json:"scope,omitempty"`

	// DayOfMonth is only populated for IntentRecurringCreate: the day of the
	// month (1-31) the transaction repeats on, as stated by the message. A
	// month shorter than this falls back to that month's own last day (see
	// recurring.Service's schedule math), so 31 safely means "end of every
	// month" including February.
	DayOfMonth int `json:"day_of_month,omitempty"`

	// ReminderOnly, also only meaningful for IntentRecurringCreate,
	// distinguishes the create_bill_reminder tool from
	// create_recurring_transaction — both populate every other field
	// identically. false (the zero value, so create_recurring_transaction
	// needs no explicit default) auto-records a real transaction each time
	// it fires; true only notifies the user, recording nothing, since the
	// user still wants to log the actual payment themselves.
	ReminderOnly bool `json:"reminder_only,omitempty"`

	// GoalName/GoalDeadlineHint are only populated for
	// IntentSavingsGoalCreate — the target amount and currency reuse
	// Amount/Currency above rather than duplicating them. GoalName is the
	// user's own free-text label (kept in their language, unlike
	// CategoryName). GoalDeadlineHint is an ISO 8601 date (YYYY-MM-DD,
	// same convention as OccurredAtHint) or empty if no deadline was
	// mentioned — a goal doesn't need one.
	GoalName         string `json:"goal_name,omitempty"`
	GoalDeadlineHint string `json:"goal_deadline_hint,omitempty"`

	// TargetTransactionID identifies which of the user's existing
	// transactions an IntentCorrect or IntentDelete result acts on, when
	// known. Populated from a get_recent_transactions candidate a
	// chained-tool-calling provider (see ChainedCorrector) echoed back
	// after matching it against what the message actually describes. Zero
	// means unspecified — the caller (transaction.Service.CorrectLast /
	// DeleteTransaction) falls back to the single most recent transaction,
	// the original behavior from before this field existed.
	TargetTransactionID uint `json:"target_transaction_id,omitempty"`

	// TargetRecurringTransactionID is IntentRecurringDelete's counterpart to
	// TargetTransactionID above — identifies which of the user's active
	// recurring transactions/bill reminders to cancel, populated from a
	// get_recurring_transactions candidate. Zero means unspecified — the
	// caller (recurring.Service.Cancel, called the same way /recurring
	// cancel <id> already does) has no single-most-recent fallback for this
	// one, since a delete with no target simply can't proceed.
	TargetRecurringTransactionID uint `json:"target_recurring_transaction_id,omitempty"`

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

	// QueryStartDate, when non-empty, OVERRIDES QueryPeriod's own default
	// start boundary with this exact ISO 8601 (YYYY-MM-DD) date — the end of
	// the range is always "now" regardless. Exists because QueryPeriod's
	// four values (daily/weekly/monthly/yearly) each anchor to a fixed
	// "current" window with no way to express "starting from a specific
	// point" (e.g. "list semua transaksi mulai dari bulan lalu" — starting
	// from last month — previously had no field to carry that, so the model
	// silently fell back to QueryPeriod="monthly", which only covers THIS
	// calendar month and dropped everything from last month entirely).
	// Resolved in bot.Service.resolveQueryRange, shared by every
	// query_spending sub-handler (total, breakdown, bucket, list,
	// uncategorized) so all of them honor it uniformly.
	QueryStartDate string `json:"query_start_date,omitempty"`

	// QueryTransactionType restricts query_spending to one transaction type
	// ("income", "investment", "transfer", "reward"), or "all" for no type
	// restriction at all. Empty means the long-standing default: "expense" —
	// every query_spending mode silently meant "spending" only until this
	// field existed, so "list semua transaksi" ("list ALL transactions")
	// actually only ever listed expenses, with no way to ask for anything
	// else. Only meaningful for the plain-total and list query modes
	// (resolveQueryTransactionType/handleSpendingListQuery) —
	// QueryCategory/QueryBucket are inherently expense/budget-specific
	// concepts in this app's model and stay expense-scoped regardless of
	// this field.
	QueryTransactionType string `json:"query_transaction_type,omitempty"`

	// QueryBucket is IntentQuery's Needs/Wants/Savings counterpart to
	// QueryCategory — set when the message asks about spending in a budget
	// bucket as a whole ("how much did I spend on Wants") rather than a
	// single named category. One of the BudgetBucket values, lowercase;
	// empty means no bucket filter. The bot layer also treats QueryCategory
	// as a bucket name if it happens to match one, as a safety net for a
	// parse that puts a bucket word here instead — see
	// bot.Service.handleSpendingQuery.
	QueryBucket string `json:"query_bucket,omitempty"`

	// QueryMode switches IntentQuery between an aggregate answer (the
	// original behavior, when empty/unrecognized) and QueryModeList — an
	// itemized listing of the individual matching transactions instead of a
	// sum, for a message like "beli apa aja buat food bulan ini?" (what did
	// I buy for food this month?) rather than "how much did I spend on
	// food?". Only meaningful together with QueryCategory/QueryBucket (or
	// neither, meaning "list everything this period").
	QueryMode string `json:"query_mode,omitempty"`

	// QueryListMore is only meaningful when QueryMode == QueryModeList: true
	// means this message is asking to continue/see more of a list the
	// assistant just showed (e.g. "lanjutkan", "show me more"), rather than
	// starting a fresh list. The model is never asked to track which page
	// comes next — bot.Service keeps a short-lived server-side cursor
	// (keyed by user + the same period/category/bucket) for that, since
	// asking a language model to do that arithmetic reliably across turns
	// is exactly the kind of thing that silently drifts wrong.
	QueryListMore bool `json:"query_list_more,omitempty"`

	// QuerySearchTerm is a free-text search over a transaction's
	// Description/RawInput — a specific merchant/place/item/person named by
	// name ("starbucks", "indomaret", "Budi"), as opposed to QueryCategory's
	// general budget-category concept ("food"). Setting this implies list
	// mode on its own (see bot.Service.handleSpendingQuery), regardless of
	// whether QueryMode was also set to QueryModeList.
	QuerySearchTerm string `json:"query_search_term,omitempty"`

	// BudgetAgainstLatestIncome/BudgetHypotheticalAmount/
	// BudgetHypotheticalCurrency are only meaningful for
	// IntentBudgetAllocation — see check_budget_allocation's own field
	// descriptions in tool_schema.go. At most one of
	// BudgetAgainstLatestIncome or a non-zero BudgetHypotheticalAmount should
	// be set; the caller (bot.Service.handleBudgetShow) treats a
	// hypothetical amount as taking priority if a parse somehow sets both.
	BudgetAgainstLatestIncome  bool    `json:"against_latest_income,omitempty"`
	BudgetHypotheticalAmount   float64 `json:"hypothetical_amount,omitempty"`
	BudgetHypotheticalCurrency string  `json:"hypothetical_currency,omitempty"`

	// BudgetSet* fields are only meaningful for IntentBudgetSet — see
	// set_budget_allocation's own field descriptions in tool_schema.go, and
	// bot.Service.handleBudgetSetFromParsed for how an amount trio gets
	// converted to percentages.
	BudgetSetNeedsPercent   float64 `json:"needs_percent,omitempty"`
	BudgetSetWantsPercent   float64 `json:"wants_percent,omitempty"`
	BudgetSetSavingsPercent float64 `json:"savings_percent,omitempty"`
	BudgetSetNeedsAmount    float64 `json:"needs_amount,omitempty"`
	BudgetSetWantsAmount    float64 `json:"wants_amount,omitempty"`
	BudgetSetSavingsAmount  float64 `json:"savings_amount,omitempty"`
	BudgetSetCurrency       string  `json:"budget_set_currency,omitempty"`
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

	// AnalyzeReceiptImage handles a receipt photo: a vision-capable call
	// extracts and records the transaction it shows. Unlike audio, every
	// current provider supports this natively, so there's no
	// capability-unsupported sentinel error for it. caption is the optional
	// text the user attached to the photo (empty if none) — treated as
	// extra context, not a separate input. Same one-or-more-results
	// contract as every other parse method, though in practice a receipt
	// should yield exactly one result (see each provider's own per-call
	// instruction, which explicitly asks for one record_transaction call
	// per receipt regardless of how many line items it lists).
	AnalyzeReceiptImage(ctx context.Context, imageBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string, caption string, history ConversationContext) ([]*ParsedTransaction, *UsageMetadata, error)

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

	// SuggestSavings writes one short, specific, actionable suggestion from
	// spendingSummary — a plain-language description of a user's last-month
	// category spending breakdown built by bot.Service.RunCategoryInsights
	// from report.Service.MonthlyCategoryInsight — in preferredLanguage.
	// Same "plain text completion, not a tool call" shape as Summarize:
	// nothing to extract, just advise.
	SuggestSavings(ctx context.Context, spendingSummary string, preferredLanguage string) (string, *UsageMetadata, error)

	// PhraseTransactionList asks the model to render an already-computed
	// list of transactions (listSummary — a plain-language, English,
	// internal description built by bot.Service.phraseSpendingList, the
	// same convention as SuggestSavings' spendingSummary) as natural
	// conversational text in preferredLanguage, instead of a rigid Sprintf
	// template. Same "plain text completion, not a tool call" shape as
	// Summarize/SuggestSavings: no transaction is parsed, decided, or
	// changed here, only reworded. The caller MUST verify every amount
	// string from listSummary still appears verbatim in the result before
	// trusting it — the model could still occasionally drop or alter a
	// detail despite the system instruction telling it not to — see
	// bot.Service.phraseSpendingList's validation for why this isn't
	// optional for a financial reply.
	PhraseTransactionList(ctx context.Context, listSummary string, preferredLanguage string) (string, *UsageMetadata, error)

	// PhraseSpendingBreakdown is PhraseTransactionList's counterpart for a
	// different already-computed data shape: a total plus its per-category
	// breakdown (breakdownSummary, same plain-language/English/internal
	// convention) rather than individual transaction rows — the plain "how
	// much have I spent" answer with a percentage breakdown, as opposed to
	// an itemized list. Same contract as PhraseTransactionList: no
	// computation happens here, only rewording, and the caller MUST verify
	// every amount/percentage string still appears verbatim before trusting
	// it — see bot.Service.phraseSpendingBreakdown's validation.
	PhraseSpendingBreakdown(ctx context.Context, breakdownSummary string, preferredLanguage string) (string, *UsageMetadata, error)

	ProviderName() string
}

// ValidateParsedTransaction is the shared schema-shape check every
// provider's response must pass before it's trusted. Query-intent responses
// only need a real period; balance- and delete-intent responses need
// nothing at all (the record-intent Type/Amount checks below don't apply to
// any of these — those fields are left zero-valued by the prompt).
// IntentCorrect isn't special-cased here since it populates the exact same
// fields as IntentRecord, so it naturally falls through to the same
// Type/Amount validation below. IntentRecurringCreate additionally requires
// a DayOfMonth in range before falling through to that same Type/Amount
// check, since it shares every other field with IntentRecord too.
func ValidateParsedTransaction(pt *ParsedTransaction) bool {
	switch pt.Intent {
	case IntentQuery:
		switch pt.QueryPeriod {
		case PeriodDaily, PeriodWeekly, PeriodMonthly, PeriodYearly:
			return true
		default:
			return false
		}
	case IntentBalance, IntentDelete, IntentSubscribe, IntentSubscriptionStatus, IntentBudgetAllocation, IntentSurplus:
		return true
	case IntentBudgetSet:
		percentSum := pt.BudgetSetNeedsPercent + pt.BudgetSetWantsPercent + pt.BudgetSetSavingsPercent
		amountSum := pt.BudgetSetNeedsAmount + pt.BudgetSetWantsAmount + pt.BudgetSetSavingsAmount
		// Only a loose "something was actually stated" shape check — the
		// authoritative sum-to-100/non-negative validation happens in
		// budget.Service.SetBudget itself (see setBudgetAndReply), the same
		// place /budget's own explicit-percentage form already relies on.
		return percentSum > 0 || amountSum > 0
	case IntentRecurringDelete:
		// Unlike IntentDelete (a regular transaction), there's no
		// "most recent" fallback concept for a recurring schedule — a
		// missing target simply can't proceed, so this is required rather
		// than optional.
		return pt.TargetRecurringTransactionID > 0
	case IntentRecurringCreate:
		if pt.DayOfMonth < 1 || pt.DayOfMonth > 31 {
			return false
		}
	case IntentSavingsGoalCreate:
		// A goal has no Type (expense/income/...) at all, so this must
		// return directly rather than falling through to the shared
		// Type/Amount switch below, which would otherwise reject every
		// goal for having an empty Type.
		return strings.TrimSpace(pt.GoalName) != "" && pt.Amount > 0
	case IntentAccountTransfer:
		// Also has no Type, so this returns directly too — both account
		// names must be given, must be different accounts (a model-level
		// guard; transaction.Service.CreateTransfer double-checks after
		// resolving both names against real accounts), and a positive
		// amount.
		from := strings.TrimSpace(pt.FromAccountName)
		to := strings.TrimSpace(pt.ToAccountName)
		return from != "" && to != "" && !strings.EqualFold(from, to) && pt.Amount > 0
	case IntentDebtLend:
		switch pt.DebtDirection {
		case "lent", "borrowed":
		default:
			return false
		}
		return strings.TrimSpace(pt.DebtCounterparty) != "" && pt.Amount > 0
	case IntentDebtRepay:
		switch pt.DebtDirection {
		case "received", "made":
		default:
			return false
		}
		return strings.TrimSpace(pt.DebtCounterparty) != "" && pt.Amount > 0
	case IntentDebtQuery:
		return true
	case IntentSetSpendingAlert:
		switch pt.AlertScope {
		case "category":
			if strings.TrimSpace(pt.CategoryName) == "" {
				return false
			}
		case "bucket":
			switch pt.Bucket {
			case "needs", "wants", "savings":
			default:
				return false
			}
		case "total":
		default:
			return false
		}
		switch pt.QueryPeriod {
		case PeriodDaily, PeriodWeekly, PeriodMonthly, PeriodYearly:
		default:
			return false
		}
		return pt.Amount > 0
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
