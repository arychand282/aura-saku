// Package factory is deliberately separate from package ai: it is the only
// place that imports every concrete provider package (gemini, openai,
// anthropic), so it can't live inside package ai itself without creating an
// import cycle (each provider imports ai for the AIClient interface/DTOs).
package factory

import (
	"fmt"
	"strings"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/anthropic"
	"aura-saku/internal/ai/gemini"
	"aura-saku/internal/ai/openai"
	"aura-saku/internal/config"
)

// NewAIClient builds the configured provider's client from cfg.AIProvider.
func NewAIClient(cfg *config.Config) (ai.AIClient, error) {
	switch strings.ToLower(cfg.AIProvider) {
	case "gemini":
		return gemini.New(cfg.GeminiAPIKey, cfg.GeminiModel), nil
	case "openai":
		return openai.New(cfg.OpenAIAPIKey, cfg.OpenAIModel), nil
	case "anthropic":
		return anthropic.New(cfg.AnthropicAPIKey, cfg.AnthropicModel), nil
	default:
		return nil, fmt.Errorf("unsupported AI_PROVIDER: %s", cfg.AIProvider)
	}
}
