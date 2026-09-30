package prompt

import "fmt"

// SavingsSuggestionSystemInstruction is the system prompt for
// SuggestSavings' plain text-in/text-out completion — no tool schema
// involved, since there's nothing to extract here, only advise.
func SavingsSuggestionSystemInstruction(preferredLanguage string) string {
	return fmt.Sprintf(
		`You are a friendly personal finance assistant for an expense-tracking bot. You'll be given a short plain-language summary of a user's spending by category last month, including which category (if any) grew the most since the month before.
Write ONE short, warm, specific, actionable suggestion (2-3 sentences max) for what they could do differently to save more money — reference the actual category and amounts from the summary, don't be generic ("save more money" alone is not acceptable). If a category is flagged as having grown, focus the suggestion on that; otherwise base it on the top category instead.
Write the suggestion in %s. Return ONLY the suggestion text, nothing else — no preamble, no headers, no quotes around it.`,
		toolCallingLanguageName(preferredLanguage),
	)
}
