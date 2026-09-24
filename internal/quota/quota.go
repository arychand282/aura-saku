// Package quota owns the daily AI-prompt quota's Redis key format and
// operations. internal/service/bot both checks and increments it — a
// single source of truth so those two call sites can never compute the key
// differently and drift out of sync.
package quota

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ttl outlives a calendar day so the key never expires a moment early near
// the UTC midnight boundary; the "which day" decision is made by Go's own
// clock at increment/check time, not by Redis's TTL expiry.
const ttl = 26 * time.Hour

// Key returns today's (UTC) daily quota counter key for a chat ID.
func Key(chatID int64) string {
	return fmt.Sprintf("aura:quota:%d:%s", chatID, time.Now().UTC().Format("2006-01-02"))
}

// CountToday returns how many successful AI prompts a chat has used today.
func CountToday(ctx context.Context, redisClient *redis.Client, chatID int64) (int, error) {
	n, err := redisClient.Get(ctx, Key(chatID)).Int()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

// Increment records one successful AI prompt against today's quota for a
// chat, setting a TTL on the key's first use each day so it self-expires
// instead of accumulating forever.
func Increment(ctx context.Context, redisClient *redis.Client, chatID int64) error {
	key := Key(chatID)
	count, err := redisClient.Incr(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("increment quota key %s: %w", key, err)
	}
	if count == 1 {
		redisClient.Expire(ctx, key, ttl)
	}
	return nil
}
