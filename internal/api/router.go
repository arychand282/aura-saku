package api

import (
	"time"

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
// full security middleware chain), auth endpoints, and the dashboard REST
// API (behind session auth).
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
		telegram.NewWebhookHandler(deps.TelegramClient, deps.BotService, deps.Config.TelegramWebhookSecret, deps.Redis),
	)

	// Auth endpoints are registered directly on app (not the session-gated
	// v1 group below) since logging in is, by definition, unauthenticated.
	sessionTTL := time.Duration(deps.Config.SessionTTLHours) * time.Hour
	authHandler := handlers.NewAuthHandler(deps.DB, deps.Redis, deps.Config.JWTSecret, sessionTTL)
	app.Post("/api/v1/auth/verify", authHandler.VerifyLoginToken)
	app.Post("/api/v1/auth/admin-login",
		middleware.IPRateLimiter(deps.Redis, deps.Config.AdminLoginRateLimitWindowSeconds, deps.Config.AdminLoginRateLimitMaxAttempts),
		authHandler.AdminLogin,
	)
	app.Post("/api/v1/auth/logout", authHandler.Logout)

	// Every other dashboard endpoint requires a verified session. A "user"
	// session is locked to its own data; an admin/superadmin session may
	// pass ?user_id= to view any user's data.
	v1 := app.Group("/api/v1",
		middleware.RequireSession(deps.Redis, deps.DB, deps.Config.JWTSecret),
		middleware.SessionRateLimiter(deps.Redis, deps.Config.DashboardRateLimitWindowSeconds, deps.Config.DashboardRateLimitMaxRequests),
	)

	txHandler := handlers.NewTransactionHandler(deps.DB)
	v1.Get("/transactions", txHandler.List)

	reportHandler := handlers.NewReportHandler(deps.DB, deps.BudgetService)
	v1.Get("/reports/summary", reportHandler.Summary)
	v1.Get("/reports/top-categories", reportHandler.TopCategories)
	v1.Get("/reports/budget-progress", reportHandler.BudgetProgress)

	exportHandler := handlers.NewExportHandler(deps.DB)
	v1.Get("/export", exportHandler.Export)
}
