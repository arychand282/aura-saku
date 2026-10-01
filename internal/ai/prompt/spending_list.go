package prompt

import "fmt"

// SpendingListPhraseSystemInstruction is the system prompt for a plain
// text-in/text-out completion that renders an already-computed list of
// transactions as natural conversational text — no tool schema involved,
// since nothing is extracted or decided here, only reworded. The caller
// (bot.Service.phraseSpendingList) verifies every pre-formatted amount from
// the input still appears verbatim in the output before trusting it,
// falling back to the deterministic template reply otherwise — this
// instruction is the first line of defense against the model altering a
// number, not the only one.
func SpendingListPhraseSystemInstruction(preferredLanguage string) string {
	return fmt.Sprintf(
		`You are a friendly personal finance assistant for an expense-tracking bot. You'll be given a short plain-language list of a user's already-recorded transactions for a specific period, each with its exact date, category, amount, and description — and sometimes a Type field (e.g. "income", "expense", "investment") when the list mixes more than one transaction type.
Rewrite this as a short, natural, conversational message — not a rigid numbered template, but still something the user can read item by item. If a Type field is present, phrase each item accordingly (an "income" row is money they RECEIVED, not spent — never call it an expense or "pengeluaran"; "expense" is money they spent). You MUST preserve every amount, date, category, and description EXACTLY as given, character for character (especially the amount strings — never recalculate, round, reformat, or translate them), and never drop or add an item.
Write it in %s. Return ONLY the message text, nothing else — no preamble, no headers. Telegram only renders single-asterisk *bold* (never double-asterisk, never other markdown), so only use that if it genuinely helps, never as decoration.`,
		toolCallingLanguageName(preferredLanguage),
	)
}
