// Package conversation owns the single, continuous Conversation per user
// and its Messages — persistence of the chat transcript, and building/
// maintaining the bounded context (recent messages + rolling summary) fed
// back to the AI on every call, so a follow-up message is understood in
// context instead of parsed as if it were the first message ever sent.
// See internal/models/conversation.go for the schema rationale.
package conversation

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/models"
)

// Config bundles the two tunables that control conversation context size
// (and therefore AI token cost) independently of business logic.
type Config struct {
	// RecentLimit is how many of the most recent messages are sent to the
	// AI verbatim on every call, on top of Conversation.Summary.
	RecentLimit int
	// SummarizeThreshold: once the number of messages newer than
	// SummarizedThroughMessageID exceeds this, MaybeSummarize folds the
	// oldest of them into Summary, keeping the newest RecentLimit as raw
	// context. Must be greater than RecentLimit, or every single turn
	// would re-trigger a summarization AI call instead of batching them.
	SummarizeThreshold int
}

type Service struct {
	db       *gorm.DB
	aiClient ai.AIClient
	cfg      Config
}

func NewService(db *gorm.DB, aiClient ai.AIClient, cfg Config) *Service {
	return &Service{db: db, aiClient: aiClient, cfg: cfg}
}

// GetOrCreate returns the user's single conversation, creating it on their
// very first message. The unique index on Conversation.UserID (see
// internal/models/conversation.go) is what makes "single" actually true
// under concurrent first-messages, not just this lookup pattern.
func (s *Service) GetOrCreate(ctx context.Context, userID uint) (*models.Conversation, error) {
	var conv models.Conversation
	if err := s.db.WithContext(ctx).Where(models.Conversation{UserID: userID}).FirstOrCreate(&conv).Error; err != nil {
		return nil, fmt.Errorf("get or create conversation: %w", err)
	}
	return &conv, nil
}

// AppendMessage records one turn. Synchronous and cheap (a single insert)
// — deliberately not fire-and-forget, so the conversation is guaranteed up
// to date by the time the next message can possibly arrive and call
// BuildContext. A blank content (e.g. a voice note transcription that came
// back empty) is silently skipped rather than storing a useless row.
func (s *Service) AppendMessage(ctx context.Context, conversationID uint, role models.MessageRole, content string) error {
	if content == "" {
		return nil
	}
	msg := models.Message{ConversationID: conversationID, Role: role, Content: content}
	if err := s.db.WithContext(ctx).Create(&msg).Error; err != nil {
		return fmt.Errorf("append message: %w", err)
	}
	return nil
}

// BuildContext returns what to feed the AI for the next call: conv's
// current summary (if any) plus its most recent messages not yet folded
// into that summary, capped at RecentLimit, oldest first (chronological —
// what a model expects a conversation transcript to look like).
func (s *Service) BuildContext(ctx context.Context, conv *models.Conversation) (ai.ConversationContext, error) {
	var msgs []models.Message
	q := s.db.WithContext(ctx).Where("conversation_id = ?", conv.ID)
	if conv.SummarizedThroughMessageID != nil {
		q = q.Where("id > ?", *conv.SummarizedThroughMessageID)
	}
	if err := q.Order("id DESC").Limit(s.cfg.RecentLimit).Find(&msgs).Error; err != nil {
		return ai.ConversationContext{}, fmt.Errorf("load recent messages: %w", err)
	}

	// msgs came back newest-first (ORDER BY id DESC LIMIT N is what makes
	// "most recent N" a cheap indexed query) — reverse into chronological
	// order here.
	turns := make([]ai.ConversationTurn, len(msgs))
	for i, m := range msgs {
		turns[len(msgs)-1-i] = ai.ConversationTurn{Role: string(m.Role), Content: m.Content}
	}

	return ai.ConversationContext{Summary: conv.Summary, Recent: turns}, nil
}

// MaybeSummarize folds the oldest unsummarized messages into conv.Summary
// once their count exceeds SummarizeThreshold, keeping the newest
// RecentLimit as raw context going forward. Safe to call after every turn
// — a no-op unless the threshold is actually crossed, so the AI
// summarization call itself only fires roughly once per RecentLimit-worth
// of new messages, not on every message. preferredLanguage controls what
// language the summary itself is written in.
func (s *Service) MaybeSummarize(ctx context.Context, conv *models.Conversation, preferredLanguage string) error {
	var unsummarized []models.Message
	q := s.db.WithContext(ctx).Where("conversation_id = ?", conv.ID)
	if conv.SummarizedThroughMessageID != nil {
		q = q.Where("id > ?", *conv.SummarizedThroughMessageID)
	}
	if err := q.Order("id ASC").Find(&unsummarized).Error; err != nil {
		return fmt.Errorf("load unsummarized messages: %w", err)
	}

	if len(unsummarized) <= s.cfg.SummarizeThreshold || len(unsummarized) <= s.cfg.RecentLimit {
		return nil
	}

	toFold := unsummarized[:len(unsummarized)-s.cfg.RecentLimit]
	if len(toFold) == 0 {
		return nil
	}

	turns := make([]ai.ConversationTurn, len(toFold))
	for i, m := range toFold {
		turns[i] = ai.ConversationTurn{Role: string(m.Role), Content: m.Content}
	}

	summary, _, err := s.aiClient.Summarize(ctx, conv.Summary, turns, preferredLanguage)
	if err != nil {
		return fmt.Errorf("summarize: %w", err)
	}

	newPointer := toFold[len(toFold)-1].ID
	if err := s.db.WithContext(ctx).Model(conv).Updates(map[string]interface{}{
		"summary":                       summary,
		"summarized_through_message_id": newPointer,
	}).Error; err != nil {
		return fmt.Errorf("save summary: %w", err)
	}
	conv.Summary = summary
	conv.SummarizedThroughMessageID = &newPointer
	return nil
}
