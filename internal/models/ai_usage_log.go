package models

import "time"

type AIProvider string

const (
	ProviderGemini    AIProvider = "gemini"
	ProviderOpenAI    AIProvider = "openai"
	ProviderAnthropic AIProvider = "anthropic"
)

// AIUsageLog is the durable ledger of every AI call (token counts, latency,
// success/failure). Redis quota counters are ephemeral (TTL'd) so this table
// is the source of truth for cost auditing and abuse investigation.
type AIUsageLog struct {
	ID       uint       `gorm:"primaryKey"`
	UserID   uint       `gorm:"not null;index"`
	Provider AIProvider `gorm:"size:20;not null"`
	Model    string     `gorm:"size:100"`
	// RequestType's size is 40, not the tighter 20 some other fields here
	// use — "phrase_spending_breakdown" alone is 25 characters, which
	// overflowed the original varchar(20) and silently failed every insert
	// for that one request type (caught in production: "value too long for
	// type character varying(20)"). Sized with headroom so the next
	// request-type label coined doesn't repeat this.
	RequestType      string    `gorm:"size:40"`
	PromptTokens     int       `gorm:"default:0"`
	CompletionTokens int       `gorm:"default:0"`
	LatencyMs        int       `gorm:"default:0"`
	Success          bool      `gorm:"default:true"`
	ErrorMessage     string    `gorm:"type:text"`
	CreatedAt        time.Time `gorm:"index"`
}
