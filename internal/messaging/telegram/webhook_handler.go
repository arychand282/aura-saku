package telegram

import (
	"context"
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"

	"aura-saku/internal/apperror"
	"aura-saku/internal/idempotency"
	"aura-saku/internal/logger"
	"aura-saku/internal/service/bot"
)

// webhookWorkerPool bounds how many webhook updates are processed
// concurrently, replacing an unbounded `go` dispatch per webhook (which
// could grow goroutines without limit under a burst of traffic). It's a
// small type local to this one caller rather than a shared package — no
// other part of this codebase needs a generic worker pool yet.
type webhookWorkerPool struct {
	jobs chan func()
}

func newWebhookWorkerPool(workers, queueSize int) *webhookWorkerPool {
	p := &webhookWorkerPool{jobs: make(chan func(), queueSize)}
	for i := 0; i < workers; i++ {
		go func() {
			for job := range p.jobs {
				job()
			}
		}()
	}
	return p
}

// submit attempts to enqueue job without blocking the HTTP handler —
// Telegram's webhook contract rewards a fast response, so a full queue
// must reject immediately (so the caller can respond 503 and let Telegram
// retry later) rather than wait for room to open up.
func (p *webhookWorkerPool) submit(job func()) bool {
	select {
	case p.jobs <- job:
		return true
	default:
		return false
	}
}

// NewWebhookHandler returns a Fiber handler for the Telegram webhook route.
// It verifies the webhook secret, parses the payload, and immediately ACKs
// 200 OK after handing the update to a bounded worker pool — all AI/DB work
// happens async in a worker, not in the HTTP handler.
//
// The idempotency check (Telegram redelivers an update if it doesn't get a
// timely 200 OK) happens INSIDE the submitted job, not before submission —
// if it happened first and the queue then turned out to be full, the
// update would be marked "seen" while never actually being processed,
// silently losing it on retry. Submitting first and checking idempotency
// once a worker has committed to the job keeps a full-queue rejection safe:
// nothing is marked seen until it's actually going to be handled.
//
// workerCount/queueSize come from config.Config.WebhookWorkerCount/
// WebhookQueueSize. The pool is created once here (NewWebhookHandler is
// called once at startup, in internal/api/router.go) and lives for the
// process's lifetime, same as every other long-running background worker
// in this codebase (see internal/scheduler).
func NewWebhookHandler(client *Client, botSvc *bot.Service, secretToken string, redisClient *redis.Client, workerCount, queueSize int) fiber.Handler {
	pool := newWebhookWorkerPool(workerCount, queueSize)

	return func(c *fiber.Ctx) error {
		if secretToken != "" && c.Get("X-Telegram-Bot-Api-Secret-Token") != secretToken {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		msg, err := client.ParseWebhookPayload(c.Body())
		if err != nil {
			if errors.Is(err, apperror.ErrUnhandledUpdateType) {
				// Expected: Telegram sends update types this bot doesn't act
				// on (edited messages, callback queries, chat-member
				// changes, ...) — not a failure, so no error log. Acking
				// 200 here (instead of 400) matters: a 400 previously made
				// Telegram retry the same update forever with exponential
				// backoff, since it never got the 2xx it was waiting for.
				return c.SendStatus(fiber.StatusOK)
			}
			logger.Error(logger.TelegramParseFailed, "telegram webhook parse payload", err)
			return c.SendStatus(fiber.StatusBadRequest)
		}

		accepted := pool.submit(func() {
			// context.Background(), not c.Context(): this job runs after
			// the HTTP handler has already returned, by which point Fiber
			// may have recycled the request's fasthttp context — the same
			// reason botSvc.HandleUpdate below already used
			// context.Background() even before this worker-pool change.
			bg := context.Background()
			isNew, err := idempotency.MarkSeen(bg, redisClient, "telegram-update", strconv.FormatInt(msg.RawUpdateID, 10), idempotency.WebhookRedeliveryTTL)
			if err != nil {
				// Fail open: a rare duplicate on a Redis hiccup is a much
				// smaller problem than dropping legitimate updates during
				// an outage.
				logger.Error(logger.RedisOpFailed, "webhook idempotency check, failing open", err)
			} else if !isNew {
				// Telegram redelivered an update we've already processed
				// (or are still processing) — skip without reprocessing.
				return
			}
			botSvc.HandleUpdate(bg, msg)
		})
		if !accepted {
			logger.Error(logger.WebhookQueueFull, "telegram webhook worker pool full, rejecting for Telegram to retry", nil)
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}

		return c.SendStatus(fiber.StatusOK)
	}
}
