package handlers

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// healthCheckTimeout bounds each dependency ping so a hung Postgres/Redis
// connection (e.g. a network partition, not just "down") fails the health
// check fast instead of hanging the request indefinitely.
const healthCheckTimeout = 2 * time.Second

type HealthHandler struct {
	db    *gorm.DB
	redis *redis.Client
}

func NewHealthHandler(db *gorm.DB, redisClient *redis.Client) *HealthHandler {
	return &HealthHandler{db: db, redis: redisClient}
}

// Check performs real pings against Postgres and Redis so a degraded
// dependency is actually reflected in the response, not hardcoded to "ok".
func (h *HealthHandler) Check(c *fiber.Ctx) error {
	// Independent timeouts per dependency — a hung Postgres shouldn't burn
	// the deadline that Redis's check would otherwise have and get
	// misreported as unavailable too.
	dbCtx, dbCancel := context.WithTimeout(c.Context(), healthCheckTimeout)
	defer dbCancel()
	dbStatus := "ok"
	if sqlDB, err := h.db.DB(); err != nil || sqlDB.PingContext(dbCtx) != nil {
		dbStatus = "unavailable"
	}

	redisCtx, redisCancel := context.WithTimeout(c.Context(), healthCheckTimeout)
	defer redisCancel()
	redisStatus := "ok"
	if err := h.redis.Ping(redisCtx).Err(); err != nil {
		redisStatus = "unavailable"
	}

	status := fiber.StatusOK
	overall := "ok"
	if dbStatus != "ok" || redisStatus != "ok" {
		status = fiber.StatusServiceUnavailable
		overall = "degraded"
	}

	return c.Status(status).JSON(fiber.Map{
		"status": overall,
		"db":     dbStatus,
		"redis":  redisStatus,
	})
}
