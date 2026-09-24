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

	// *BaseURL fields override each provider's API endpoint (empty means the
	// client uses its own built-in default) — useful for Azure OpenAI,
	// Bedrock/Vertex-hosted Claude, self-hosted OpenAI-compatible gateways
	// (LiteLLM/vLLM/Ollama), or a corporate proxy, without a code change.
	GeminiAPIKey  string
	GeminiModel   string
	GeminiBaseURL string

	OpenAIAPIKey  string
	OpenAIModel   string
	OpenAIBaseURL string

	AnthropicAPIKey     string
	AnthropicModel      string
	AnthropicBaseURL    string
	AnthropicAPIVersion string

	// FXProviderBaseURL overrides the live exchange-rate provider's base
	// URL (empty means fx.LiveService uses its own built-in default) — kept
	// configurable because the provider has already changed domains once.
	FXProviderBaseURL string

	MaxInputChars          int
	RateLimitWindowSeconds int
	RateLimitMaxRequests   int
	FreeTierDailyQuota     int
	ProTierDailyQuota      int

	// RewardAffiliateURL enables the free-tier "earn bonus quota" flow when
	// set (empty disables it entirely — no reward is offered without a real
	// link configured). See internal/reward.
	RewardAffiliateURL    string
	RewardBonusAmount     int
	RewardMaxClaimsPerDay int

	// JWTSecret signs dashboard session tokens — required, since signing
	// with an empty/weak key would let anyone forge a session.
	JWTSecret       string
	SessionTTLHours int

	// DashboardBaseURL is used to build the login link the bot sends via
	// /dashboard (empty means that command replies "not available yet").
	DashboardBaseURL string

	AdminLoginRateLimitWindowSeconds int
	AdminLoginRateLimitMaxAttempts   int

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

		GeminiAPIKey:  getEnv("GEMINI_API_KEY", ""),
		GeminiModel:   getEnv("GEMINI_MODEL", "gemini-2.0-flash"),
		GeminiBaseURL: getEnv("GEMINI_BASE_URL", ""),

		OpenAIAPIKey:  getEnv("OPENAI_API_KEY", ""),
		OpenAIModel:   getEnv("OPENAI_MODEL", "gpt-4o-mini"),
		OpenAIBaseURL: getEnv("OPENAI_BASE_URL", ""),

		AnthropicAPIKey:     getEnv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:      getEnv("ANTHROPIC_MODEL", "claude-sonnet-5"),
		AnthropicBaseURL:    getEnv("ANTHROPIC_BASE_URL", ""),
		AnthropicAPIVersion: getEnv("ANTHROPIC_API_VERSION", ""),

		FXProviderBaseURL: getEnv("FX_PROVIDER_BASE_URL", ""),

		MaxInputChars:          getEnvInt("MAX_INPUT_CHARS", 200),
		RateLimitWindowSeconds: getEnvInt("RATE_LIMIT_WINDOW_SECONDS", 10),
		RateLimitMaxRequests:   getEnvInt("RATE_LIMIT_MAX_REQUESTS", 5),
		FreeTierDailyQuota:     getEnvInt("FREE_TIER_DAILY_QUOTA", 3),
		ProTierDailyQuota:      getEnvInt("PRO_TIER_DAILY_QUOTA", -1),

		RewardAffiliateURL:    getEnv("REWARD_AFFILIATE_URL", ""),
		RewardBonusAmount:     getEnvInt("REWARD_BONUS_AMOUNT", 3),
		RewardMaxClaimsPerDay: getEnvInt("REWARD_MAX_CLAIMS_PER_DAY", 1),

		JWTSecret:       getEnv("JWT_SECRET", ""),
		SessionTTLHours: getEnvInt("SESSION_TTL_HOURS", 168),

		DashboardBaseURL: getEnv("DASHBOARD_BASE_URL", ""),

		AdminLoginRateLimitWindowSeconds: getEnvInt("ADMIN_LOGIN_RATE_LIMIT_WINDOW_SECONDS", 300),
		AdminLoginRateLimitMaxAttempts:   getEnvInt("ADMIN_LOGIN_RATE_LIMIT_MAX_ATTEMPTS", 5),

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
		"JWT_SECRET":  c.JWTSecret,
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
