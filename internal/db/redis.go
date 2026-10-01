package db

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"aura-saku/internal/config"
)

// redisOptionsFromConfig builds the *redis.Options NewRedis connects with —
// split out from NewRedis so the URL-vs-separate-fields branching can be
// tested without needing a live connection (ParseURL's TLS inference for
// Upstash's "rediss://" scheme is exactly the part worth protecting against
// a silent regression). cfg.RedisURL takes priority over
// Host/Port/Password/DB when non-empty.
func redisOptionsFromConfig(cfg *config.Config) (*redis.Options, error) {
	if cfg.RedisURL != "" {
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("parse REDIS_URL: %w", err)
		}
		return opts, nil
	}
	return &redis.Options{
		Addr:     fmt.Sprintf("%s:%s", cfg.RedisHost, cfg.RedisPort),
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	}, nil
}

func NewRedis(cfg *config.Config) (*redis.Client, error) {
	opts, err := redisOptionsFromConfig(cfg)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
