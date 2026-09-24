package middleware

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"

	"aura-saku/internal/apperror"
	"aura-saku/internal/logger"
	"aura-saku/internal/messaging"
)

// RateLimiter is a Redis-backed fixed-window burst guard per Telegram chat
// ID: INCR the window counter, set its TTL on first hit, and reject once the
// count exceeds maxRequests within windowSeconds. On Redis errors this fails
// OPEN (allows the request through) so a Redis outage doesn't take down the
// whole bot — the daily quota check downstream remains the hard cost cap.
func RateLimiter(redisClient *redis.Client, windowSeconds, maxRequests int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		msg, ok := c.Locals(IncomingMessageLocalsKey).(*messaging.IncomingMessage)
		if !ok {
			return c.Next()
		}

		ctx := c.Context()
		key := fmt.Sprintf("aura:ratelimit:%d", msg.ChatID)

		count, err := redisClient.Incr(ctx, key).Result()
		if err != nil {
			logger.Error(logger.RedisOpFailed, "rate limiter redis incr, failing open", err)
			return c.Next()
		}
		if count == 1 {
			redisClient.Expire(ctx, key, time.Duration(windowSeconds)*time.Second)
		}

		if int(count) > maxRequests {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": apperror.ErrRateLimited.Error(),
			})
		}

		return c.Next()
	}
}

// SessionRateLimiter is the same fixed-window guard as RateLimiter, keyed
// by the session's effective user ID (set by RequireSession, which must run
// first) instead of client IP. Dashboard traffic is a Next.js BFF proxying
// server-to-server, so every request shares one origin IP — an IP-keyed
// limiter would only ever throttle the whole dashboard at once instead of
// the single user actually generating load.
func SessionRateLimiter(redisClient *redis.Client, windowSeconds, maxRequests int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals(LocalsEffectiveUserID).(uint)
		ctx := c.Context()
		key := fmt.Sprintf("aura:dashboardlimit:%d", userID)

		count, err := redisClient.Incr(ctx, key).Result()
		if err != nil {
			logger.Error(logger.RedisOpFailed, "dashboard rate limiter redis incr, failing open", err)
			return c.Next()
		}
		if count == 1 {
			redisClient.Expire(ctx, key, time.Duration(windowSeconds)*time.Second)
		}

		if int(count) > maxRequests {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": apperror.ErrRateLimited.Error(),
			})
		}

		return c.Next()
	}
}

// IPRateLimiter is the same fixed-window guard as RateLimiter, keyed by
// client IP instead of Telegram chat ID — for HTTP endpoints with no
// IncomingMessage in play, like the admin login form, where brute-forcing a
// password is the actual threat being guarded against.
func IPRateLimiter(redisClient *redis.Client, windowSeconds, maxRequests int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := c.Context()
		key := fmt.Sprintf("aura:iplimit:%s", c.IP())

		count, err := redisClient.Incr(ctx, key).Result()
		if err != nil {
			logger.Error(logger.RedisOpFailed, "ip rate limiter redis incr, failing open", err)
			return c.Next()
		}
		if count == 1 {
			redisClient.Expire(ctx, key, time.Duration(windowSeconds)*time.Second)
		}

		if int(count) > maxRequests {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": apperror.ErrRateLimited.Error(),
			})
		}

		return c.Next()
	}
}
