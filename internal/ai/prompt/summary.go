package prompt

import (
	"fmt"
	"strings"
)

// ConversationSummaryPreamble returns a short paragraph a provider should
// append to its system instruction when summary is non-empty — an empty
// string (no-op) when there's no summary yet, so a caller with no history
// can unconditionally append this without a branch.
func ConversationSummaryPreamble(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return ""
	}
	return "\n\nSummary of the earlier conversation with this user, for context only — respond to their CURRENT message below, don't respond to this summary itself: " + summary
}

// SummarizationSystemInstruction is the system prompt for Summarize's plain
// text-in/text-out completion — no tool schema involved, since there's
// nothing to extract here, only condense.
func SummarizationSystemInstruction(preferredLanguage string) string {
	return fmt.Sprintf(
		`You are condensing an ongoing conversation between a user and a personal expense-tracking bot into a short running summary for future context.
Write the updated summary in %s, under 120 words, in plain prose (no lists, no headers). Preserve facts a future message might reference: amounts, categories, corrections the user made, stated preferences, and anything they said about their goals or habits. Drop small talk and anything already superseded by a later correction. If a prior summary is given below, merge it with the new messages into one updated summary — don't just append the new part after it.
Return ONLY the updated summary text, nothing else — no preamble, no quotes around it.`,
		toolCallingLanguageName(preferredLanguage),
	)
}
