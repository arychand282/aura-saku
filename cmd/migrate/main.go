package main

import (
	"log"

	"aura-saku/internal/config"
	"aura-saku/internal/db"
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

	if err := db.RunMigrations(gormDB); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	log.Println("migrations applied successfully")
}
