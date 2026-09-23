package middleware

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/messaging"
	"aura-saku/internal/models"
)

// DailyQuota enforces the free-tier daily AI-prompt cap (pro tier is
// unlimited). Tier is cached in Redis (aura:usertier:{chatID}, 5m TTL) to
// avoid a DB round trip on every message; on a cache miss it falls back to
// Postgres, defaulting to free tier if the user doesn't exist yet (first
// message ever).
//
// NOTE: this middleware only checks the ceiling before letting the request
// through. Incrementing aura:quota:{chatID}:{date} happens downstream, in
// bot.Service, only after a successful AI-parsed response — a rejected or
// failed request should not consume quota.
func DailyQuota(redisClient *redis.Client, db *gorm.DB, freeQuota int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		msg, ok := c.Locals(IncomingMessageLocalsKey).(*messaging.IncomingMessage)
		if !ok {
			return c.Next()
		}

		ctx := c.Context()
		tier := resolveTier(ctx, redisClient, db, msg.ChatID)

		if tier == models.TierPro {
			return c.Next()
		}

		dateKey := time.Now().UTC().Format("2006-01-02")
		quotaKey := fmt.Sprintf("aura:quota:%d:%s", msg.ChatID, dateKey)

		count, err := redisClient.Get(ctx, quotaKey).Int()
		if err != nil && err != redis.Nil {
			log.Printf("daily quota: redis get failed, failing open: %v", err)
			return c.Next()
		}

		if count >= freeQuota {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": apperror.ErrQuotaExceeded.Error(),
			})
		}

		return c.Next()
	}
}

func resolveTier(ctx context.Context, redisClient *redis.Client, db *gorm.DB, chatID int64) models.SubscriptionTier {
	tierKey := fmt.Sprintf("aura:usertier:%d", chatID)

	cached, err := redisClient.Get(ctx, tierKey).Result()
	if err == nil && cached != "" {
		return models.SubscriptionTier(cached)
	}

	var user models.User
	tier := models.TierFree
	if dbErr := db.Where("telegram_chat_id = ?", chatID).First(&user).Error; dbErr == nil {
		tier = user.Tier
	}

	redisClient.Set(ctx, tierKey, string(tier), 5*time.Minute)
	return tier
}
