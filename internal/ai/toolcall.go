package ai

import "aura-saku/internal/ai/prompt"

// ParsedTransactionFromToolCall maps a native function/tool call — a tool
// name plus its already-decoded argument map — onto the shared
// ParsedTransaction shape ValidateParsedTransaction and bot.Service
// already expect. Shared by all three native-function-calling providers
// (gemini/openai/anthropic) once each has reduced its own wire response
// (Gemini's args map, OpenAI's JSON-string arguments after one
// json.Unmarshal, Anthropic's input object) down to a plain
// map[string]interface{} — this is the one place that mapping logic lives
// instead of being duplicated three times.
func ParsedTransactionFromToolCall(name string, args map[string]interface{}) *ParsedTransaction {
	switch name {
	case prompt.ToolRecordTransaction:
		pt := transactionFromArgs(args)
		pt.Intent = IntentRecord
		return pt
	case prompt.ToolCorrectTransaction:
		pt := transactionFromArgs(args)
		pt.Intent = IntentCorrect
		return pt
	case prompt.ToolQuerySpending:
		return &ParsedTransaction{
			Intent:        IntentQuery,
			QueryPeriod:   argString(args, "query_period"),
			QueryCategory: argString(args, "query_category"),
		}
	case prompt.ToolGetBalance:
		return &ParsedTransaction{Intent: IntentBalance}
	default:
		return nil
	}
}

func transactionFromArgs(args map[string]interface{}) *ParsedTransaction {
	return &ParsedTransaction{
		Type:           argString(args, "type"),
		Amount:         argFloat(args, "amount"),
		Currency:       argString(args, "currency"),
		CategoryName:   argString(args, "category_name"),
		Bucket:         argString(args, "bucket"),
		Description:    argString(args, "description"),
		OccurredAtHint: argString(args, "occurred_at_hint"),
	}
}

func argString(args map[string]interface{}, key string) string {
	s, _ := args[key].(string)
	return s
}

func argFloat(args map[string]interface{}, key string) float64 {
	f, _ := args[key].(float64)
	return f
}
