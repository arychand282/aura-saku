package api

import (
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/api/handlers"
	"aura-saku/internal/config"
	"aura-saku/internal/messaging/telegram"
	"aura-saku/internal/middleware"
	"aura-saku/internal/service/bot"
	"aura-saku/internal/service/budget"
)

// Deps bundles everything the router needs to wire up routes and their
// middleware chains.
type Deps struct {
	DB             *gorm.DB
	Redis          *redis.Client
	Config         *config.Config
	TelegramClient *telegram.Client
	BotService     *bot.Service
	BudgetService  *budget.Service
}

// Setup mounts every route: health check, the Telegram webhook (behind the
// full security middleware chain), and the dashboard REST API.
func Setup(app *fiber.App, deps Deps) {
	app.Use(middleware.Recover())
	app.Use(middleware.RequestLogger())

	healthHandler := handlers.NewHealthHandler(deps.DB, deps.Redis)
	app.Get("/healthz", healthHandler.Check)

	// Daily AI-prompt quota is enforced downstream in bot.Service (not here)
	// so a blocked user gets an actual reply — including the earn-bonus-quota
	// offer — rather than a silent webhook-level rejection.
	app.Post("/webhook/telegram",
		middleware.LimitInputLength(deps.TelegramClient, deps.Config.MaxInputChars),
		middleware.RateLimiter(deps.Redis, deps.Config.RateLimitWindowSeconds, deps.Config.RateLimitMaxRequests),
		telegram.NewWebhookHandler(deps.TelegramClient, deps.BotService, deps.Config.TelegramWebhookSecret),
	)

	v1 := app.Group("/api/v1")

	txHandler := handlers.NewTransactionHandler(deps.DB)
	v1.Get("/transactions", txHandler.List)

	reportHandler := handlers.NewReportHandler(deps.DB, deps.BudgetService)
	v1.Get("/reports/summary", reportHandler.Summary)
	v1.Get("/reports/top-categories", reportHandler.TopCategories)
	v1.Get("/reports/budget-progress", reportHandler.BudgetProgress)

	exportHandler := handlers.NewExportHandler(deps.DB)
	v1.Get("/export", exportHandler.Export)
}
