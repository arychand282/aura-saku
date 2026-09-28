package models

import "time"

// Conversation is the single, continuous chat thread for a user (see the
// unique index on UserID) — it never splits into separate sessions.
// Instead, Summary absorbs everything older than
// SummarizedThroughMessageID, so the context a caller sends the AI on
// every call stays bounded no matter how long the user has been chatting.
// See internal/service/conversation for how Summary gets built and kept
// up to date.
type Conversation struct {
	ID     uint `gorm:"primaryKey"`
	UserID uint `gorm:"uniqueIndex;not null"`

	// Summary is an AI-generated condensation of every message older than
	// SummarizedThroughMessageID. Empty until the conversation grows past
	// the summarization threshold.
	Summary string `gorm:"type:text"`
	// SummarizedThroughMessageID is the highest Message.ID already folded
	// into Summary — nil means nothing has been summarized yet. Messages
	// with ID greater than this are the "recent" raw turns still sent to
	// the AI verbatim, on top of Summary.
	SummarizedThroughMessageID *uint `gorm:"index"`

	CreatedAt time.Time
	UpdatedAt time.Time

	Messages []Message `gorm:"foreignKey:ConversationID"`
}
