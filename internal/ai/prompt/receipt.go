package prompt

import "fmt"

// ReceiptAnalysisInstruction is the instruction sent alongside a receipt
// photo, reused by every provider's AnalyzeReceiptImage. It deliberately
// overrides TransactionTools' general "call record_transaction once per
// distinct transaction" guidance (written for text messages listing
// genuinely separate real-world purchases) for this one call: a receipt is
// a single checkout event no matter how many line items it lists.
// caption is the optional text the user attached to the photo — appended as
// extra context when non-empty, omitted entirely otherwise.
func ReceiptAnalysisInstruction(caption string) string {
	instruction := `Analyze this receipt photo and extract the transaction it represents. This is ONE payment event (a single checkout) even if it lists multiple line items — call record_transaction ONCE for the total amount actually paid, not once per item. Briefly summarize what was purchased in the description (store name and main items), the way the user would describe it themselves. Extract the date from the receipt if legible (occurred_at_hint); otherwise assume today. If the image is unreadable, doesn't look like a receipt, or you can't confidently determine the total paid, do NOT call record_transaction — ask a short clarifying question instead.`
	if caption != "" {
		instruction += fmt.Sprintf("\nThe user attached this note with the photo: %q — treat it as additional context (e.g. what the purchase was for).", caption)
	}
	return instruction
}
