package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

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
	dbStatus := "ok"
	if sqlDB, err := h.db.DB(); err != nil || sqlDB.Ping() != nil {
		dbStatus = "unavailable"
	}

	redisStatus := "ok"
	if err := h.redis.Ping(c.Context()).Err(); err != nil {
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
