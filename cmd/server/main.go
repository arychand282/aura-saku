package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"

	"aura-saku/internal/ai/factory"
	"aura-saku/internal/api"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/logger"
	"aura-saku/internal/messaging/telegram"
	"aura-saku/internal/service/bot"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/transaction"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal(logger.CfgLoadFailed, "config load", err)
	}

	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		logger.Fatal(logger.DBConnectFailed, "postgres connect", err)
	}

	redisClient, err := db.NewRedis(cfg)
	if err != nil {
		logger.Fatal(logger.RedisConnectFailed, "redis connect", err)
	}

	if err := db.RunMigrations(gormDB); err != nil {
		logger.Fatal(logger.DBMigrationFailed, "run migrations", err)
	}

	aiClient, err := factory.NewAIClient(cfg)
	if err != nil {
		logger.Fatal(logger.AIClientBuildFailed, "build AI client", err)
	}

	telegramClient := telegram.New(cfg.TelegramBotToken)

	fxSvc := fx.NewStaticService()
	txSvc := transaction.NewService(gormDB, fxSvc)
	budgetSvc := budget.NewService(gormDB)
	reconSvc := reconciliation.NewService(gormDB, txSvc)
	botSvc := bot.NewService(gormDB, telegramClient, aiClient, txSvc, budgetSvc, reconSvc)

	app := fiber.New(fiber.Config{
		AppName: "aura-saku",
	})

	api.Setup(app, api.Deps{
		DB:             gormDB,
		Redis:          redisClient,
		Config:         cfg,
		TelegramClient: telegramClient,
		BotService:     botSvc,
		BudgetService:  budgetSvc,
	})

	go func() {
		addr := ":" + cfg.AppPort
		log.Printf("aura-saku listening on %s (env=%s, ai_provider=%s)", addr, cfg.AppEnv, aiClient.ProviderName())
		if err := app.Listen(addr); err != nil {
			logger.Fatal(logger.ServerListenFailed, "fiber listen", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		logger.Error(logger.ServerShutdownFailed, "graceful shutdown", err)
	}
	log.Println("server stopped")
}
