// Package usage persists the audit trail for AI calls: a per-call
// AIUsageLog row (token counts, latency) and a daily QuotaUsage mirror.
// Redis (internal/quota) is the real-time enforcement path; these tables
// exist for cost auditing, abuse investigation, and billing disputes after
// the fact — so writes here are best-effort and never block a reply.
package usage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/logger"
	"aura-saku/internal/models"
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// RecordAICall persists an AIUsageLog row and increments today's QuotaUsage
// SuccessCount for the user. Intended to be called fire-and-forget (in its
// own goroutine) from bot.Service so neither write can delay the user's
// reply — failures are logged here, not returned, since there's no
// meaningful caller left to hand an error to.
func (s *Service) RecordAICall(ctx context.Context, userID uint, provider models.AIProvider, requestType string, meta *ai.UsageMetadata) {
	entry := &models.AIUsageLog{
		UserID:      userID,
		Provider:    provider,
		RequestType: requestType,
		Success:     true,
	}
	if meta != nil {
		entry.Model = meta.Model
		entry.PromptTokens = meta.PromptTokens
		entry.CompletionTokens = meta.CompletionTokens
		entry.LatencyMs = meta.LatencyMs
	}
	if err := s.db.WithContext(ctx).Create(entry).Error; err != nil {
		logger.Error(logger.DBQueryFailed, "insert AI usage log", err)
	}

	if err := s.incrementQuotaUsage(ctx, userID); err != nil {
		logger.Error(logger.DBQueryFailed, "increment quota usage", err)
	}
}

func (s *Service) incrementQuotaUsage(ctx context.Context, userID uint) error {
	today := time.Now().UTC()
	usageDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	var row models.QuotaUsage
	err := s.db.WithContext(ctx).
		Where(models.QuotaUsage{UserID: userID, UsageDate: usageDate}).
		FirstOrCreate(&row).Error
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&row).UpdateColumn("success_count", gorm.Expr("success_count + 1")).Error
}
