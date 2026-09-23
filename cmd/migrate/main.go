package main

import (
	"log"

	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/logger"
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

	if err := db.RunMigrations(gormDB); err != nil {
		logger.Fatal(logger.DBMigrationFailed, "run migrations", err)
	}

	log.Println("migrations applied successfully")
}
