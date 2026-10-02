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
	// RedisURL, when set, takes priority over RedisHost/Port/Password/DB
	// above — a single connection URL (e.g. Upstash's "rediss://...") that
	// also carries whether to use TLS, which the separate fields have no
	// way to express. Local/Docker Redis has no TLS, so this stays empty
	// and the separate fields are used instead; a managed provider that
	// requires TLS (Upstash, Redis Cloud, ...) sets this instead.
	RedisURL string

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

	// MidtransServerKey/MidtransClientKey configure the Pro-tier checkout
	// flow (see internal/service/payment) — same "empty disables it
	// entirely" convention as RewardAffiliateURL above: /subscribe replies
	// "not available yet" rather than failing outright when unset.
	// MidtransIsProduction defaults false (sandbox) so this ships safely
	// testable before any real key is ever configured. MidtransActive is a
	// separate manual kill switch, independent of whether keys are
	// present — set false to force /subscribe "not available yet" without
	// touching real credentials (e.g. to pause new signups temporarily).
	// payment.Service treats both conditions the same way: either one being
	// false means unavailable.
	MidtransServerKey    string
	MidtransClientKey    string
	MidtransIsProduction bool
	MidtransActive       bool

	AdminLoginRateLimitWindowSeconds int
	AdminLoginRateLimitMaxAttempts   int

	// DashboardRateLimit guards the session-authenticated dashboard API
	// (/api/v1/transactions, /reports/*, /export), keyed per effective user
	// ID rather than IP — see middleware.SessionRateLimiter.
	DashboardRateLimitWindowSeconds int
	DashboardRateLimitMaxRequests   int

	// ConversationRecentLimit/SummarizeThreshold bound how much conversation
	// history is sent to the AI on every call (and therefore its token
	// cost) — see internal/service/conversation.Config's own doc comments
	// for the exact roles of each.
	ConversationRecentLimit        int
	ConversationSummarizeThreshold int

	// WebhookWorkerCount/WebhookQueueSize bound how many Telegram webhook
	// updates are processed concurrently (see
	// internal/messaging/telegram.NewWebhookHandler) — replaces an
	// unbounded `go` dispatch per webhook, which could grow goroutines
	// without limit under a burst of traffic.
	WebhookWorkerCount int
	WebhookQueueSize   int

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
		RedisURL:      getEnv("REDIS_URL", ""),

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

		MaxInputChars:          getEnvInt("MAX_INPUT_CHARS", 500),
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

		MidtransServerKey:    getEnv("MIDTRANS_SERVER_KEY", ""),
		MidtransClientKey:    getEnv("MIDTRANS_CLIENT_KEY", ""),
		MidtransIsProduction: getEnvBool("MIDTRANS_IS_PRODUCTION", false),
		MidtransActive:       getEnvBool("MIDTRANS_ACTIVE", true),

		AdminLoginRateLimitWindowSeconds: getEnvInt("ADMIN_LOGIN_RATE_LIMIT_WINDOW_SECONDS", 300),
		AdminLoginRateLimitMaxAttempts:   getEnvInt("ADMIN_LOGIN_RATE_LIMIT_MAX_ATTEMPTS", 5),

		DashboardRateLimitWindowSeconds: getEnvInt("DASHBOARD_RATE_LIMIT_WINDOW_SECONDS", 60),
		DashboardRateLimitMaxRequests:   getEnvInt("DASHBOARD_RATE_LIMIT_MAX_REQUESTS", 120),

		ConversationRecentLimit:        getEnvInt("CONVERSATION_RECENT_LIMIT", 10),
		ConversationSummarizeThreshold: getEnvInt("CONVERSATION_SUMMARIZE_THRESHOLD", 20),

		WebhookWorkerCount: getEnvInt("WEBHOOK_WORKER_COUNT", 20),
		WebhookQueueSize:   getEnvInt("WEBHOOK_QUEUE_SIZE", 200),

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

func getEnvBool(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}
