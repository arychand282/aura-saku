package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/auth"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/alert"
	"aura-saku/internal/service/fx"
	"aura-saku/internal/service/transaction"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	redisClient, err := db.NewRedis(cfg)
	if err != nil {
		log.Fatalf("connect redis: %v", err)
	}

	ctx := context.Background()
	user := models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree, IsActive: true}
	if err := gormDB.Create(&user).Error; err != nil {
		log.Fatalf("create test user: %v", err)
	}

	txSvc := transaction.NewService(gormDB, fx.NewStaticService())
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "income", Amount: 1_000_000, Currency: "IDR", CategoryName: "salary", AccountName: "BCA",
	}, models.SourceText, "gaji 1 juta ke BCA"); err != nil {
		log.Fatalf("seed income: %v", err)
	}
	if _, err := txSvc.CreateFromParsed(ctx, user.ID, user.DefaultCurrency, &ai.ParsedTransaction{
		Type: "expense", Amount: 50_000, Currency: "IDR", CategoryName: "food", Description: "makan siang di warung",
	}, models.SourceText, "makan siang 50rb"); err != nil {
		log.Fatalf("seed expense: %v", err)
	}
	if _, _, err := txSvc.CreateLoan(ctx, user.ID, user.DefaultCurrency, "Budi", models.DebtOwedToMe, 200_000, "IDR", "", "", ""); err != nil {
		log.Fatalf("seed loan: %v", err)
	}
	if _, err := alert.Create(ctx, gormDB, user.ID, models.AlertScopeCategory, "food", nil, 100_000, "monthly"); err != nil {
		log.Fatalf("seed alert: %v", err)
	}

	sessionTTL := time.Duration(cfg.SessionTTLHours) * time.Hour
	token, err := auth.CreateSession(ctx, redisClient, cfg.JWTSecret, sessionTTL, auth.RoleUser, user.ID, 0)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}

	fmt.Printf("USER_ID=%d\nTOKEN=%s\n", user.ID, token)
}
