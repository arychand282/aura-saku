package models

import "time"

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
)

// Message is one turn in a Conversation — either what the user actually
// sent (raw text, or "[voice note]" for an untranscribed voice message,
// the same convention as Transaction.RawInput) or what the bot replied.
// Every interaction is stored here, including commands (e.g. "/undo") and
// their replies, for a complete transcript — see
// internal/service/conversation.Service.BuildContext for which of these
// actually get fed back to the AI as context (a bounded recent window plus
// Conversation.Summary, not the whole table every time).
type Message struct {
	ID             uint        `gorm:"primaryKey"`
	ConversationID uint        `gorm:"not null;index"`
	Role           MessageRole `gorm:"size:10;not null;check:chk_messages_role,role IN ('user','assistant')"`
	Content        string      `gorm:"type:text;not null"`
	CreatedAt      time.Time   `gorm:"index"`
}
