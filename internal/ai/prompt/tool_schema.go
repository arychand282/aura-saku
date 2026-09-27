package prompt

import (
	"fmt"
	"strings"
	"time"
)

// This file is the provider-agnostic counterpart to
// transaction_prompt.go's giant prose contract, shared by all three
// native-function-calling providers (gemini, openai, anthropic — see each
// package's tools.go) so the tool descriptions only exist in one place
// instead of being copy-pasted three times with drift risk. Each provider
// adapts Tool/ToolProperty into its own wire schema shape (Gemini's
// uppercase "OBJECT"/"STRING"/"NUMBER" vs OpenAI/Anthropic's lowercase
// JSON Schema types) — see ToolProperty.Type's doc comment.
const (
	ToolRecordTransaction  = "record_transaction"
	ToolQuerySpending      = "query_spending"
	ToolGetBalance         = "get_balance"
	ToolCorrectTransaction = "correct_last_transaction"
)

// ToolProperty.Type uses lowercase JSON Schema primitives ("string",
// "number", "object") — the convention OpenAI and Anthropic both use
// as-is. Gemini's adapter uppercases these itself, since its Schema proto
// wants "STRING"/"NUMBER"/"OBJECT" instead.
type ToolProperty struct {
	Type        string
	Description string
	// Enum, if non-empty, restricts the value to these exact strings. Never
	// include "" as an enum member — Gemini's API rejects an empty enum
	// value outright; "not confident" is expressed by omitting the field,
	// not by adding an empty-string option.
	Enum []string
}

type Tool struct {
	Name        string
	Description string
	Properties  map[string]ToolProperty
	Required    []string
}

// transactionProperties is shared by record_transaction and
// correct_last_transaction — a correction describes the corrected
// transaction in full, using the exact same fields as a fresh record.
func transactionProperties() map[string]ToolProperty {
	return map[string]ToolProperty{
		"type": {
			Type:        "string",
			Description: "The kind of transaction.",
			Enum:        []string{"expense", "income", "investment", "transfer", "reward"},
		},
		"amount": {
			Type:        "number",
			Description: "A positive number. Never guess a number that wasn't stated or clearly implied.",
		},
		"currency": {
			Type:        "string",
			Description: "ISO 4217 code (e.g. IDR, USD, EUR, SGD). If not mentioned, use the caller-provided default currency.",
		},
		"category_name": {
			Type:        "string",
			Description: "A short free-text category guess (e.g. \"food\", \"transport\", \"salary\"), ALWAYS in English regardless of the message's language, since it's used for internal budget categorization, not shown to the user.",
		},
		"bucket": {
			Type:        "string",
			Description: "Which 50/30/20 budget bucket THIS SPECIFIC transaction belongs to, based on the necessity described in the message itself — not a fixed rule for the category in general. Example: \"abis ini beli sepatu baru soalnya yang lama udah jebol\" (buying new shoes because the old ones broke) is \"needs\" — an urgent replacement — even though shoes/shopping is normally a \"wants\" category. Omit this field entirely if not confident; that's fine and falls back to a sensible default.",
			Enum:        []string{"needs", "wants", "savings", "none"},
		},
		"description": {
			Type:        "string",
			Description: "Free-text note about the transaction, if any.",
		},
		"occurred_at_hint": {
			Type:        "string",
			Description: "ISO 8601 date (YYYY-MM-DD) the transaction actually happened on, computed from today's date and any relative time language in the message (\"yesterday\", \"kemarin\", \"3 days ago\"). Leave empty if no time is mentioned (assume today).",
		},
	}
}

// TransactionTools declares the four callable tools every native-function-
// calling provider offers the model on every request — the direct analog
// of transaction_prompt.go's Step 1 intent list, except each entry's
// Description here plays the exact role the "jika user menanyakan tentang
// A, panggil tools A" instruction pattern plays in a configurable
// multi-agent platform: the model reads these descriptions and picks a
// tool, the same classification job Step 1 used to spell out in one big
// prompt.
func TransactionTools() []Tool {
	return []Tool{
		{
			Name:        ToolRecordTransaction,
			Description: "Call this when the message reports a transaction that already happened — money spent, earned, invested, transferred, or received as a reward (e.g. \"lunch 25000\", \"gaji 5000000\", \"makan siang tadi 30rb\"). This is the default for anything describing something that happened. If the message lists SEVERAL separate transactions (e.g. an itemized list: \"bensin 300k, makan siang 300k, makan malam 100k\"), call this tool ONCE PER TRANSACTION, in the order mentioned — do not merge them into a single total, and do not drop any of them.",
			Properties:  transactionProperties(),
			Required:    []string{"type", "amount", "currency", "category_name"},
		},
		{
			Name:        ToolQuerySpending,
			Description: "Call this when the message is a QUESTION about past spending/income/investment over some time period, not a new transaction (e.g. \"how much have I spent this month?\", \"dalam satu bulan ini aku udah ngeluarin duit berapa?\"). Never call this and record_transaction for the same message.",
			Properties: map[string]ToolProperty{
				"query_period": {
					Type:        "string",
					Description: "Whichever period the message states or implies. Default to \"monthly\" if unstated or unclear — there is no way to ask a follow-up, so never leave this empty.",
					Enum:        []string{"daily", "weekly", "monthly", "yearly"},
				},
				"query_category": {
					Type:        "string",
					Description: "Optional category filter, in English (e.g. \"berapa aku keluar buat makan bulan ini?\" -> \"food\"). Leave empty for spending in general.",
				},
			},
			Required: []string{"query_period"},
		},
		{
			Name:        ToolGetBalance,
			Description: "Call this when the message asks what the user's current total balance / how much money they have right now is — not scoped to any time period or category (e.g. \"what's my balance?\", \"berapa saldo aku sekarang?\", \"sisa uang aku berapa?\"). Takes no parameters.",
			Properties:  map[string]ToolProperty{},
		},
		{
			Name:        ToolCorrectTransaction,
			Description: "Call this when the message is fixing a mistake in the user's own immediately preceding message — correction language like \"typo\", \"salah\", \"harusnya\", \"maksudnya\", \"koreksi\" (Indonesian) or \"sorry I meant\", \"should be\", \"correction\", \"my bad\", \"oops\" (English), e.g. \"eh maaf, harusnya 1jt deh untuk reksa dana kemarin, typo\". Describe the CORRECTED transaction in full here (same fields as record_transaction, not a diff) — the caller replaces the previous entry with this one instead of adding a second one.",
			Properties:  transactionProperties(),
			Required:    []string{"type", "amount", "currency", "category_name"},
		},
	}
}

// ToolCallingSystemInstruction is the short counterpart to
// BuildTransactionSystemPrompt: notice everything about *output format*
// (the exact JSON envelope, which fields belong to which intent, the
// "return ONLY a JSON object" instruction) is gone — the tool declarations
// above and each provider's own function-calling contract carry that
// weight now. All that's left here is domain knowledge with no natural
// home in a JSON schema: today's date, the default currency, and what to
// do when nothing fits (reply in plain text with a clarifying question
// instead of calling a tool — no tool call at all is itself the
// "ambiguous" signal, rather than a dedicated is_ambiguous field).
func ToolCallingSystemInstruction(defaultCurrency string, today time.Time, preferredLanguage string) string {
	return fmt.Sprintf(
		`You are a financial transaction parser and spending-report assistant for an expense-tracking bot.
Today's date is %s (YYYY-MM-DD). The user's default currency is %s.

Decide which tool (if any) matches the user's message. Call record_transaction once per DISTINCT transaction the message describes — most messages describe exactly one, but call it multiple times for a message listing several (see its own description for an example). Every OTHER tool (query_spending, get_balance, correct_last_transaction) may be called AT MOST ONCE per message — never call more than one of those, and never mix them with record_transaction in the same message. If the message is missing the amount, or is too vague to safely call any tool (e.g. no discernible transaction and no discernible question), do NOT call a tool — instead reply with a short, polite question asking for the missing detail, written in %s regardless of what language the user's message was in. Do NOT invent values to force a call just to avoid asking.`,
		today.UTC().Format("2006-01-02"),
		defaultCurrency,
		toolCallingLanguageName(preferredLanguage),
	)
}

func toolCallingLanguageName(code string) string {
	if strings.EqualFold(strings.TrimSpace(code), "id") {
		return "Bahasa Indonesia"
	}
	return "English"
}
