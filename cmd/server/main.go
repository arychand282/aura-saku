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
	"aura-saku/internal/scheduler"
	"aura-saku/internal/service/bot"
	"aura-saku/internal/service/budget"
	"aura-saku/internal/service/conversation"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/reconciliation"
	"aura-saku/internal/service/recurring"
	"aura-saku/internal/service/report"
	"aura-saku/internal/service/savingsgoal"
	"aura-saku/internal/service/transaction"
	"aura-saku/internal/service/usage"
)

// rolloverNudgeInterval controls how often RunBudgetRolloverNudges ticks,
// not how often any single user is actually messaged — that's governed by
// its own per-user, per-calendar-month idempotency check (see
// bot.Service.RunBudgetRolloverNudges), so ticking daily just means a
// surplus from the 1st of the month is noticed (and sent) within a day
// instead of requiring exact midnight timing.
const rolloverNudgeInterval = 24 * time.Hour

// recurringTransactionsInterval controls how often RunRecurringTransactions
// checks for due schedules. Hourly rather than daily (unlike the rollover
// nudge above) so a rent/subscription posts within the hour of its due
// date instead of being up to a day late.
const recurringTransactionsInterval = 1 * time.Hour

// categoryInsightInterval controls how often RunCategoryInsights checks
// whether a user's monthly recap is due — its own constant (not reusing
// rolloverNudgeInterval, even though the value matches) so each job's
// cadence can be tuned independently later.
const categoryInsightInterval = 24 * time.Hour

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

	fxSvc := fx.NewLiveService(cfg.FXProviderBaseURL, fx.NewStaticService())
	txSvc := transaction.NewService(gormDB, fxSvc)
	budgetSvc := budget.NewService(gormDB)
	reconSvc := reconciliation.NewService(gormDB, txSvc)
	usageSvc := usage.NewService(gormDB)
	reportSvc := report.NewService(gormDB)
	convSvc := conversation.NewService(gormDB, aiClient, conversation.Config{
		RecentLimit:        cfg.ConversationRecentLimit,
		SummarizeThreshold: cfg.ConversationSummarizeThreshold,
	})
	recurSvc := recurring.NewService(gormDB)
	goalSvc := savingsgoal.NewService(gormDB, budgetSvc)
	botSvc := bot.NewService(gormDB, redisClient, telegramClient, aiClient, txSvc, budgetSvc, reconSvc, usageSvc, reportSvc, convSvc, recurSvc, goalSvc, bot.Config{
		FreeTierDailyQuota:    cfg.FreeTierDailyQuota,
		MaxInputChars:         cfg.MaxInputChars,
		RewardAffiliateURL:    cfg.RewardAffiliateURL,
		RewardBonusAmount:     cfg.RewardBonusAmount,
		RewardMaxClaimsPerDay: cfg.RewardMaxClaimsPerDay,
		DashboardBaseURL:      cfg.DashboardBaseURL,
	})

	schedulerCtx, cancelScheduler := context.WithCancel(context.Background())
	scheduler.New(
		scheduler.Job{
			Name:     "budget-rollover-nudge",
			Interval: rolloverNudgeInterval,
			Run:      botSvc.RunBudgetRolloverNudges,
		},
		scheduler.Job{
			Name:     "recurring-transactions",
			Interval: recurringTransactionsInterval,
			Run:      botSvc.RunRecurringTransactions,
		},
		scheduler.Job{
			Name:     "category-insights",
			Interval: categoryInsightInterval,
			Run:      botSvc.RunCategoryInsights,
		},
	).Start(schedulerCtx)

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
		ReportService:  reportSvc,
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
	cancelScheduler()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(ctx); err != nil {
		logger.Error(logger.ServerShutdownFailed, "graceful shutdown", err)
	}
	log.Println("server stopped")
}
