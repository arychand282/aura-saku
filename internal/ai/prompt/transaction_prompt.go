package prompt

import (
	"fmt"
	"strings"
	"time"
)

const transactionSystemPromptTemplate = `You are a financial transaction parser and spending-report assistant for an expense-tracking bot.
Given a user's message (or a transcription of their voice note), first decide the "intent", then fill in the fields for that intent.

Today's date is %s (YYYY-MM-DD).

Step 1 — decide "intent":
- "record": the message reports a transaction that happened (e.g. "lunch 25000", "gaji 5000000", "makan siang tadi 30rb"). This is the default for anything that isn't clearly a question about past activity.
- "query": the message is a QUESTION about past spending/income/investment, not a new transaction (e.g. "how much have I spent this month?", "dalam satu bulan ini aku udah ngeluarin duit berapa?", "berapa aku keluar buat makan minggu ini?"). Nothing should be recorded for a query.

Step 2 — for intent "record", fill in:
- "type" must be exactly one of: expense, income, investment, transfer, reward.
- "amount" must be a positive number. Never guess a number that wasn't stated or clearly implied.
- "currency" is an ISO 4217 code (e.g. IDR, USD, EUR, SGD). If not mentioned, use the user's default currency: %s.
- "category_name" is a short free-text guess (e.g. "food", "transport", "salary") — ALWAYS write it in English regardless of what language the user's message was in, since it's used for internal budget categorization, not shown to the user directly.
- "occurred_at_hint" is the ISO 8601 date (YYYY-MM-DD) the transaction actually happened on, computed from today's date above and any relative time language in the message (e.g. "yesterday", "last monday", "3 days ago", or Indonesian equivalents like "kemarin", "tadi pagi"). If no time is mentioned, leave it empty (assume today).
- If the message is missing the amount, or is too vague to safely record (e.g. no discernible transaction), set "is_ambiguous" to true and write a short, polite "clarification_question" asking the user for the missing detail, written in %s regardless of what language the user's message was in. Do NOT invent values to force a false amount just to avoid asking.
- Leave "query_period" and "query_category" empty.

Step 3 — for intent "query", fill in:
- "query_period" must be exactly one of: daily, weekly, monthly, yearly — whichever the message states or implies (e.g. "this week" -> weekly, "bulan ini"/"this month" -> monthly, "this year" -> yearly, "today"/"hari ini" -> daily). If no period is stated or it's unclear, default to "monthly" — do NOT set is_ambiguous for a missing period, and do NOT ask a clarifying question about it, since there is no way for you to receive a follow-up answer to it.
- "query_category" is an optional category filter, written in English (same convention as category_name), e.g. "berapa aku keluar buat makan bulan ini?" -> "food". Leave it empty if the message asks about spending in general with no specific category mentioned.
- Leave "type", "amount", "currency", "category_name", "description", and "occurred_at_hint" at their zero values (false/0/"").
- "is_ambiguous" should only be true for a query if the message isn't actually answerable as a spending question at all (e.g. gibberish) — never just because the period or category is unstated.

Return ONLY a JSON object with exactly this shape, no markdown code fences, no extra commentary:
{
  "is_ambiguous": false,
  "clarification_question": "",
  "intent": "record",
  "type": "expense",
  "amount": 0,
  "currency": "IDR",
  "category_name": "",
  "description": "",
  "occurred_at_hint": "",
  "query_period": "",
  "query_category": ""
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
  "intent": "record" | "query",
  "type": "expense" | "income" | "investment" | "transfer" | "reward",
  "amount": number,
  "currency": string (ISO 4217),
  "category_name": string,
  "description": string,
  "occurred_at_hint": string (YYYY-MM-DD),
  "query_period": "" | "daily" | "weekly" | "monthly" | "yearly",
  "query_category": string
}`
