// Package idempotency provides a generic "have we seen this before" check
// backed by Redis SET NX. Its first use is guarding against a messaging
// platform redelivering the same webhook update — Telegram retries if it
// doesn't get a timely 200 OK, and without this, a redelivered "coffee
// 20000" gets parsed and saved as a second, duplicate transaction. Kept
// generic (namespace + id, not Telegram-specific) so a future WhatsApp
// webhook can reuse it under its own namespace without key collisions.
package idempotency

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ttl covers a messaging platform's redelivery window with margin —
// Telegram's own retry window is well under this.
const ttl = 24 * time.Hour

// MarkSeen atomically records that (namespace, id) has been processed.
// Returns true the first time a given pair is seen (safe to process), or
// false if it's a duplicate that should be skipped without reprocessing.
func MarkSeen(ctx context.Context, redisClient *redis.Client, namespace, id string) (bool, error) {
	key := fmt.Sprintf("aura:seen:%s:%s", namespace, id)
	ok, err := redisClient.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("mark seen %s: %w", key, err)
	}
	return ok, nil
}
