package gemini

import (
	"strings"
	"time"

	"aura-saku/internal/ai/prompt"
)

// transactionTools adapts the shared prompt.TransactionTools() declarations
// (also used by the openai and anthropic providers — see their own
// tools.go) into Gemini's request wire shape. The only real translation
// work is uppercasing JSON Schema type names — Gemini's Schema proto wants
// "OBJECT"/"STRING"/"NUMBER", not the lowercase "object"/"string"/"number"
// OpenAI and Anthropic both accept as-is.
func transactionTools() []genTool {
	tools := prompt.TransactionTools()
	decls := make([]functionDeclaration, len(tools))
	for i, t := range tools {
		props := make(map[string]propertySchema, len(t.Properties))
		for name, p := range t.Properties {
			props[name] = propertySchema{
				Type:        strings.ToUpper(p.Type),
				Description: p.Description,
				Enum:        p.Enum,
			}
		}
		decls[i] = functionDeclaration{
			Name:        t.Name,
			Description: t.Description,
			Parameters: functionSchema{
				Type:       "OBJECT",
				Properties: props,
				Required:   t.Required,
			},
		}
	}
	return []genTool{{FunctionDeclarations: decls}}
}

func toolCallingSystemInstruction(defaultCurrency string, today time.Time, preferredLanguage string) string {
	return prompt.ToolCallingSystemInstruction(defaultCurrency, today, preferredLanguage)
}
