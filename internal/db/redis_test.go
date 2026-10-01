package db

import (
	"testing"

	"aura-saku/internal/config"
)

// TestRedisOptionsFromConfig_URLTakesPriorityWithTLS covers the Upstash
// case: a "rediss://" REDIS_URL must win over the separate Host/Port
// fields and must come back with TLS configured — go-redis's ParseURL only
// sets Options.TLSConfig for the "rediss" scheme, never for "redis", so
// this is the one thing actually worth protecting here (a managed Redis
// requiring TLS, like Upstash, will fail to connect at all without it).
func TestRedisOptionsFromConfig_URLTakesPriorityWithTLS(t *testing.T) {
	cfg := &config.Config{
		RedisHost: "should-be-ignored",
		RedisPort: "9999",
		RedisURL:  "rediss://default:secret@up-stash-example.io:6379",
	}

	opts, err := redisOptionsFromConfig(cfg)
	if err != nil {
		t.Fatalf("redisOptionsFromConfig: %v", err)
	}
	if opts.Addr != "up-stash-example.io:6379" {
		t.Errorf("Addr = %q, want the host/port from REDIS_URL, not RedisHost/RedisPort", opts.Addr)
	}
	if opts.Password != "secret" {
		t.Errorf("Password = %q, want %q", opts.Password, "secret")
	}
	if opts.TLSConfig == nil {
		t.Error("TLSConfig = nil, want non-nil for a rediss:// URL (Upstash requires TLS)")
	}
}

// TestRedisOptionsFromConfig_PlainURLHasNoTLS covers the non-Upstash case:
// a plain "redis://" URL (no TLS) must not get a TLSConfig either — this
// isn't the primary bug being guarded against, but confirms the TLS
// inference is scheme-driven, not unconditional.
func TestRedisOptionsFromConfig_PlainURLHasNoTLS(t *testing.T) {
	cfg := &config.Config{RedisURL: "redis://localhost:6379"}

	opts, err := redisOptionsFromConfig(cfg)
	if err != nil {
		t.Fatalf("redisOptionsFromConfig: %v", err)
	}
	if opts.TLSConfig != nil {
		t.Error("TLSConfig != nil for a plain redis:// URL, want nil")
	}
}

// TestRedisOptionsFromConfig_FallsBackToSeparateFields covers local/Docker
// Redis — the pre-existing behavior, unchanged, when RedisURL is empty.
func TestRedisOptionsFromConfig_FallsBackToSeparateFields(t *testing.T) {
	cfg := &config.Config{
		RedisHost:     "localhost",
		RedisPort:     "6379",
		RedisPassword: "",
		RedisDB:       2,
	}

	opts, err := redisOptionsFromConfig(cfg)
	if err != nil {
		t.Fatalf("redisOptionsFromConfig: %v", err)
	}
	if opts.Addr != "localhost:6379" {
		t.Errorf("Addr = %q, want %q", opts.Addr, "localhost:6379")
	}
	if opts.DB != 2 {
		t.Errorf("DB = %d, want 2", opts.DB)
	}
	if opts.TLSConfig != nil {
		t.Error("TLSConfig != nil for local Redis with no REDIS_URL, want nil")
	}
}

// TestRedisOptionsFromConfig_InvalidURL covers a malformed REDIS_URL
// failing clearly rather than silently falling back to the (likely wrong)
// separate fields.
func TestRedisOptionsFromConfig_InvalidURL(t *testing.T) {
	cfg := &config.Config{RedisURL: "not-a-valid-url::://"}

	if _, err := redisOptionsFromConfig(cfg); err == nil {
		t.Error("redisOptionsFromConfig err = nil for a malformed REDIS_URL, want an error")
	}
}
