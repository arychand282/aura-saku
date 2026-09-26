package anthropic

import "aura-saku/internal/ai/prompt"

// transactionTools adapts the shared prompt.TransactionTools() declarations
// (also used by the gemini and openai providers — see their own tools.go)
// into Anthropic's Messages API request wire shape: tools are declared at
// the top level (not nested under a "function" key like OpenAI's), and the
// parameter schema field is named "input_schema" rather than "parameters".
// No type-name translation is needed here either — Anthropic's JSON Schema
// already uses the lowercase "object"/"string"/"number" convention
// prompt.ToolProperty stores natively.
func transactionTools() []toolDef {
	tools := prompt.TransactionTools()
	defs := make([]toolDef, len(tools))
	for i, t := range tools {
		props := make(map[string]propertySchema, len(t.Properties))
		for name, p := range t.Properties {
			props[name] = propertySchema{
				Type:        p.Type,
				Description: p.Description,
				Enum:        p.Enum,
			}
		}
		defs[i] = toolDef{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: inputSchema{
				Type:       "object",
				Properties: props,
				Required:   t.Required,
			},
		}
	}
	return defs
}
