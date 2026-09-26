package gemini

import (
	"fmt"
	"strings"
	"time"
)

// This file is the native-function-calling counterpart to
// internal/ai/prompt/transaction_prompt.go. Instead of describing the
// entire output contract in prose and asking the model to hand-write JSON
// text matching it (which then has to be parsed, schema-validated, and
// possibly repair-retried on this side), each "intent" from that prompt
// becomes its own declared tool with its own real JSON schema. The model
// decides which tool (if any) to call the same way it always does for
// intent classification — it's still the model's judgement call, just
// expressed as a structured functionCall part instead of free-form JSON
// text we have to parse ourselves.
const (
	toolRecordTransaction  = "record_transaction"
	toolQuerySpending      = "query_spending"
	toolGetBalance         = "get_balance"
	toolCorrectTransaction = "correct_last_transaction"
)

// transactionSchemaProperties is shared by record_transaction and
// correct_last_transaction — a correction describes the corrected
// transaction in full, using the exact same fields as a fresh record.
func transactionSchemaProperties() map[string]propertySchema {
	return map[string]propertySchema{
		"type": {
			Type:        "STRING",
			Description: "The kind of transaction.",
			Enum:        []string{"expense", "income", "investment", "transfer", "reward"},
		},
		"amount": {
			Type:        "NUMBER",
			Description: "A positive number. Never guess a number that wasn't stated or clearly implied.",
		},
		"currency": {
			Type:        "STRING",
			Description: "ISO 4217 code (e.g. IDR, USD, EUR, SGD). If not mentioned, use the caller-provided default currency.",
		},
		"category_name": {
			Type:        "STRING",
			Description: "A short free-text category guess (e.g. \"food\", \"transport\", \"salary\"), ALWAYS in English regardless of the message's language, since it's used for internal budget categorization, not shown to the user.",
		},
		"bucket": {
			Type:        "STRING",
			Description: "Which 50/30/20 budget bucket THIS SPECIFIC transaction belongs to, based on the necessity described in the message itself — not a fixed rule for the category in general. Example: \"abis ini beli sepatu baru soalnya yang lama udah jebol\" (buying new shoes because the old ones broke) is \"needs\" — an urgent replacement — even though shoes/shopping is normally a \"wants\" category. Omit this field entirely if not confident; that's fine and falls back to a sensible default.",
			Enum:        []string{"needs", "wants", "savings", "none"},
		},
		"description": {
			Type:        "STRING",
			Description: "Free-text note about the transaction, if any.",
		},
		"occurred_at_hint": {
			Type:        "STRING",
			Description: "ISO 8601 date (YYYY-MM-DD) the transaction actually happened on, computed from today's date and any relative time language in the message (\"yesterday\", \"kemarin\", \"3 days ago\"). Leave empty if no time is mentioned (assume today).",
		},
	}
}

// transactionTools declares the four callable tools available to the model
// on every request — the direct analog of transaction_prompt.go's Step 1
// intent list, except each entry's Description here plays the exact role
// the "jika user menanyakan tentang A, panggil tools A" instruction pattern
// plays in a configurable multi-agent platform: the model reads these
// descriptions and picks a tool, the same classification job Step 1 used to
// spell out in one big prompt.
func transactionTools() []genTool {
	return []genTool{{
		FunctionDeclarations: []functionDeclaration{
			{
				Name:        toolRecordTransaction,
				Description: "Call this when the message reports a transaction that already happened — money spent, earned, invested, transferred, or received as a reward (e.g. \"lunch 25000\", \"gaji 5000000\", \"makan siang tadi 30rb\"). This is the default for anything describing something that happened.",
				Parameters: functionSchema{
					Type:       "OBJECT",
					Properties: transactionSchemaProperties(),
					Required:   []string{"type", "amount", "currency", "category_name"},
				},
			},
			{
				Name:        toolQuerySpending,
				Description: "Call this when the message is a QUESTION about past spending/income/investment over some time period, not a new transaction (e.g. \"how much have I spent this month?\", \"dalam satu bulan ini aku udah ngeluarin duit berapa?\"). Never call this and record_transaction for the same message.",
				Parameters: functionSchema{
					Type: "OBJECT",
					Properties: map[string]propertySchema{
						"query_period": {
							Type:        "STRING",
							Description: "Whichever period the message states or implies. Default to \"monthly\" if unstated or unclear — there is no way to ask a follow-up, so never leave this empty.",
							Enum:        []string{"daily", "weekly", "monthly", "yearly"},
						},
						"query_category": {
							Type:        "STRING",
							Description: "Optional category filter, in English (e.g. \"berapa aku keluar buat makan bulan ini?\" -> \"food\"). Leave empty for spending in general.",
						},
					},
					Required: []string{"query_period"},
				},
			},
			{
				Name:        toolGetBalance,
				Description: "Call this when the message asks what the user's current total balance / how much money they have right now is — not scoped to any time period or category (e.g. \"what's my balance?\", \"berapa saldo aku sekarang?\", \"sisa uang aku berapa?\"). Takes no parameters.",
				Parameters: functionSchema{
					Type:       "OBJECT",
					Properties: map[string]propertySchema{},
				},
			},
			{
				Name:        toolCorrectTransaction,
				Description: "Call this when the message is fixing a mistake in the user's own immediately preceding message — correction language like \"typo\", \"salah\", \"harusnya\", \"maksudnya\", \"koreksi\" (Indonesian) or \"sorry I meant\", \"should be\", \"correction\", \"my bad\", \"oops\" (English), e.g. \"eh maaf, harusnya 1jt deh untuk reksa dana kemarin, typo\". Describe the CORRECTED transaction in full here (same fields as record_transaction, not a diff) — the caller replaces the previous entry with this one instead of adding a second one.",
				Parameters: functionSchema{
					Type:       "OBJECT",
					Properties: transactionSchemaProperties(),
					Required:   []string{"type", "amount", "currency", "category_name"},
				},
			},
		},
	}}
}

// toolCallingSystemInstruction is the short counterpart to
// prompt.BuildTransactionSystemPrompt: notice everything about *output
// format* (the exact JSON envelope, which fields belong to which intent,
// the "return ONLY a JSON object" instruction) is gone — the tool
// declarations above and the API's own function-calling contract carry
// that weight now. All that's left here is domain knowledge that has no
// natural home in a JSON schema: today's date, the default currency, and
// what to do when nothing fits (reply in plain text with a clarifying
// question instead of calling a tool — no tool call at all is itself the
// "ambiguous" signal, rather than a dedicated is_ambiguous field).
func toolCallingSystemInstruction(defaultCurrency string, today time.Time, preferredLanguage string) string {
	return fmt.Sprintf(
		`You are a financial transaction parser and spending-report assistant for an expense-tracking bot.
Today's date is %s (YYYY-MM-DD). The user's default currency is %s.

Decide which tool (if any) matches the user's message and call exactly one. If the message is missing the amount, or is too vague to safely call any tool (e.g. no discernible transaction and no discernible question), do NOT call a tool — instead reply with a short, polite question asking for the missing detail, written in %s regardless of what language the user's message was in. Do NOT invent values to force a call just to avoid asking.`,
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
