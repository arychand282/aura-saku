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
		log.Fatalf("config load: %v", err)
	}

	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		log.Fatalf("postgres connect: %v", err)
	}

	redisClient, err := db.NewRedis(cfg)
	if err != nil {
		log.Fatalf("redis connect: %v", err)
	}

	if err := db.RunMigrations(gormDB); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	aiClient, err := factory.NewAIClient(cfg)
	if err != nil {
		log.Fatalf("build AI client: %v", err)
	}

	telegramClient := telegram.New(cfg.TelegramBotToken)

	fxSvc := fx.NewStaticService()
	txSvc := transaction.NewService(gormDB, fxSvc)
	budgetSvc := budget.NewService(gormDB)
	reconSvc := reconciliation.NewService(gormDB)
	botSvc := bot.NewService(gormDB, telegramClient, aiClient, txSvc, reconSvc)

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
			log.Fatalf("fiber listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("shutting down gracefully...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("server stopped")
}
