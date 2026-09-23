package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv  string
	AppPort string

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	RedisHost     string
	RedisPort     string
	RedisPassword string
	RedisDB       int

	TelegramBotToken      string
	TelegramWebhookURL    string
	TelegramWebhookSecret string

	AIProvider string

	GeminiAPIKey string
	GeminiModel  string

	OpenAIAPIKey string
	OpenAIModel  string

	AnthropicAPIKey string
	AnthropicModel  string

	MaxInputChars          int
	RateLimitWindowSeconds int
	RateLimitMaxRequests   int
	FreeTierDailyQuota     int
	ProTierDailyQuota      int

	LogLevel string
}

// Load reads configuration from the environment (and a local .env file if
// present, ignored silently if missing e.g. in production containers where
// env vars are injected directly), then validates required fields.
func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		AppEnv:  getEnv("APP_ENV", "development"),
		AppPort: getEnv("APP_PORT", "8080"),

		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", ""),
		DBPassword: getEnv("DB_PASSWORD", ""),
		DBName:     getEnv("DB_NAME", ""),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		RedisHost:     getEnv("REDIS_HOST", "localhost"),
		RedisPort:     getEnv("REDIS_PORT", "6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvInt("REDIS_DB", 0),

		TelegramBotToken:      getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramWebhookURL:    getEnv("TELEGRAM_WEBHOOK_URL", ""),
		TelegramWebhookSecret: getEnv("TELEGRAM_WEBHOOK_SECRET", ""),

		AIProvider: getEnv("AI_PROVIDER", "gemini"),

		GeminiAPIKey: getEnv("GEMINI_API_KEY", ""),
		GeminiModel:  getEnv("GEMINI_MODEL", "gemini-2.0-flash"),

		OpenAIAPIKey: getEnv("OPENAI_API_KEY", ""),
		OpenAIModel:  getEnv("OPENAI_MODEL", "gpt-4o-mini"),

		AnthropicAPIKey: getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:  getEnv("ANTHROPIC_MODEL", "claude-sonnet-5"),

		MaxInputChars:          getEnvInt("MAX_INPUT_CHARS", 200),
		RateLimitWindowSeconds: getEnvInt("RATE_LIMIT_WINDOW_SECONDS", 10),
		RateLimitMaxRequests:   getEnvInt("RATE_LIMIT_MAX_REQUESTS", 5),
		FreeTierDailyQuota:     getEnvInt("FREE_TIER_DAILY_QUOTA", 3),
		ProTierDailyQuota:      getEnvInt("PRO_TIER_DAILY_QUOTA", -1),

		LogLevel: getEnv("LOG_LEVEL", "debug"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	required := map[string]string{
		"DB_USER":     c.DBUser,
		"DB_PASSWORD": c.DBPassword,
		"DB_NAME":     c.DBName,
	}
	for name, val := range required {
		if val == "" {
			return fmt.Errorf("missing required env var: %s", name)
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return i
}
