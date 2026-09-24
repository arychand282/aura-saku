package prompt

import (
	"fmt"
	"strings"
	"time"
)

const transactionSystemPromptTemplate = `You are a financial transaction parser for an expense-tracking bot.
Given a user's message (or a transcription of their voice note), extract a single financial transaction.

Today's date is %s (YYYY-MM-DD).

Rules:
- "type" must be exactly one of: expense, income, investment, transfer, reward.
- "amount" must be a positive number. Never guess a number that wasn't stated or clearly implied.
- "currency" is an ISO 4217 code (e.g. IDR, USD, EUR, SGD). If not mentioned, use the user's default currency: %s.
- "category_name" is a short free-text guess (e.g. "food", "transport", "salary") — ALWAYS write it in English regardless of what language the user's message was in, since it's used for internal budget categorization, not shown to the user directly.
- "occurred_at_hint" is the ISO 8601 date (YYYY-MM-DD) the transaction actually happened on, computed from today's date above and any relative time language in the message (e.g. "yesterday", "last monday", "3 days ago", or Indonesian equivalents like "kemarin", "tadi pagi"). If no time is mentioned, leave it empty (assume today).
- If the message is missing the amount, or is too vague to safely record (e.g. no discernible transaction), set "is_ambiguous" to true and write a short, polite "clarification_question" asking the user for the missing detail, written in %s regardless of what language the user's message was in. Do NOT invent values to force a false amount just to avoid asking.

Return ONLY a JSON object with exactly this shape, no markdown code fences, no extra commentary:
{
  "is_ambiguous": false,
  "clarification_question": "",
  "type": "expense",
  "amount": 0,
  "currency": "IDR",
  "category_name": "",
  "description": "",
  "occurred_at_hint": ""
}`

// BuildTransactionSystemPrompt renders the system prompt every provider
// sends before the user's message. Centralizing the fmt.Sprintf here (rather
// than each provider client calling it with positional args) means the
// argument order can never drift between gemini/openai/anthropic.
//
// preferredLanguage is a plain code ("id"/"en"); this package intentionally
// doesn't import internal/i18n or internal/models to stay a leaf dependency
// of every provider client — anything other than "id" is treated as English.
func BuildTransactionSystemPrompt(defaultCurrency string, today time.Time, preferredLanguage string) string {
	return fmt.Sprintf(
		transactionSystemPromptTemplate,
		today.UTC().Format("2006-01-02"),
		defaultCurrency,
		languageName(preferredLanguage),
	)
}

func languageName(code string) string {
	if strings.EqualFold(strings.TrimSpace(code), "id") {
		return "Bahasa Indonesia"
	}
	return "English"
}

// TransactionJSONSchema documents the exact contract internal/ai.ParsedTransaction
// expects back from every provider. Kept as a doc string rather than a real
// JSON-schema struct since providers do their own regex/repair parsing
// (see ai.StripJSONFences / ai.ValidateParsedTransaction) instead of
// provider-side schema enforcement.
const TransactionJSONSchema = `{
  "is_ambiguous": boolean,
  "clarification_question": string,
  "type": "expense" | "income" | "investment" | "transfer" | "reward",
  "amount": number,
  "currency": string (ISO 4217),
  "category_name": string,
  "description": string,
  "occurred_at_hint": string (YYYY-MM-DD)
}`
