package ai

// ConversationTurn is one prior turn of the ongoing conversation, fed back
// to the model as real history — not a demo/example. Role is a plain
// string ("user"/"assistant", matching models.MessageRole's values) rather
// than importing internal/models, so this package stays a leaf dependency
// every provider client already relies on.
type ConversationTurn struct {
	Role    string
	Content string
}

// ConversationContext is what a caller (bot.Service, via
// internal/service/conversation) supplies so the model can continue a
// conversation instead of treating every message as the first one ever
// sent. Summary stands in for everything older than Recent, keeping the
// context bounded regardless of how long the conversation has run — see
// conversation.Service.BuildContext for how these two pieces get produced.
// The zero value (both fields empty) means "no history" — every method
// that accepts a ConversationContext must behave exactly as it did before
// this parameter existed when given the zero value, so existing callers
// and tests aren't forced to construct one.
type ConversationContext struct {
	Summary string
	Recent  []ConversationTurn
}
