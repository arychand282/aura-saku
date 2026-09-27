package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/prompt"
)

const toolGetLastTransaction = "get_last_transaction"

// maxChainRounds bounds how many times ParseCorrectionChained will call the
// API in a single request — see the identical constant/comment on
// gemini.Client.ParseCorrectionChained.
const maxChainRounds = 4

// correctionChainTools is the shared 4 tools (transactionTools) plus a 5th
// one only this method offers: get_last_transaction. Deliberately NOT
// added to the shared prompt.TransactionTools() list every provider uses —
// see gemini/chain.go's identical comment for why.
func correctionChainTools() []toolDef {
	tools := transactionTools()
	return append(tools, toolDef{
		Type: "function",
		Function: functionDef{
			Name:        toolGetLastTransaction,
			Description: "Call this BEFORE correct_last_transaction if the correction message doesn't restate every field of the original transaction (e.g. \"eh salah, harusnya 1 juta aja\" only gives a corrected amount, not a category) — use its result to fill in whatever correct_last_transaction still needs. Takes no parameters. Skip this entirely if the correction message already states type/amount/currency/category on its own.",
			Parameters:  functionParameters{Type: "object", Properties: map[string]propertySchema{}},
		},
	})
}

// ParseCorrectionChained is OpenAI's counterpart to
// gemini.Client.ParseCorrectionChained — same mechanism (a loop resending
// an ever-growing message list, no special "continue" API), different wire
// shape: the model's tool-call turn is replayed back as an assistant
// message carrying ToolCalls, and the tool's result is a "tool"-role
// message keyed by ToolCallID rather than a name-keyed functionResponse
// part like Gemini's.
func (c *Client) ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, lookup ai.LastTransactionLookup) (*ai.ParsedTransaction, *ai.UsageMetadata, []ai.ChainStep, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	tools := correctionChainTools()

	messages := []chatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "User message: " + userText},
	}

	var steps []ai.ChainStep
	usage := &ai.UsageMetadata{Model: c.model}
	start := time.Now()

	for round := 0; round < maxChainRounds; round++ {
		resp, err := c.doChatRequest(ctx, chatRequest{Model: c.model, Messages: messages, Tools: tools})
		if err != nil {
			return nil, nil, steps, err
		}
		if len(resp.Choices) == 0 {
			break
		}
		usage.PromptTokens += resp.Usage.PromptTokens
		usage.CompletionTokens += resp.Usage.CompletionTokens

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			usage.LatencyMs = int(time.Since(start).Milliseconds())
			if msg.Content == "" {
				return nil, nil, steps, fmt.Errorf("empty response with no tool call")
			}
			return &ai.ParsedTransaction{IsAmbiguous: true, ClarificationQuestion: msg.Content}, usage, steps, nil
		}

		tc := msg.ToolCalls[0]
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return nil, nil, steps, fmt.Errorf("unmarshal tool call arguments: %w", err)
		}

		if tc.Function.Name == toolGetLastTransaction {
			result, lookupErr := lookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_last_transaction lookup: %w", lookupErr)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: tc.Function.Name, Args: args, Result: result})

			resultJSON, jsonErr := json.Marshal(ai.LastTransactionToMap(result))
			if jsonErr != nil {
				return nil, nil, steps, fmt.Errorf("marshal tool result: %w", jsonErr)
			}
			// Replay the model's own tool-call turn (ToolCalls, no Content)
			// then the real result keyed by ToolCallID — OpenAI correlates
			// a tool message to its call by this ID, not by name/position.
			messages = append(messages,
				chatMessage{Role: "assistant", ToolCalls: msg.ToolCalls},
				chatMessage{Role: "tool", ToolCallID: tc.ID, Content: string(resultJSON)},
			)
			continue
		}

		steps = append(steps, ai.ChainStep{ToolCalled: tc.Function.Name, Args: args})
		pt := ai.ParsedTransactionFromToolCall(tc.Function.Name, args)
		if pt == nil || !ai.ValidateParsedTransaction(pt) {
			return nil, nil, steps, fmt.Errorf("model called unrecognized/invalid tool %q", tc.Function.Name)
		}
		usage.LatencyMs = int(time.Since(start).Milliseconds())
		return pt, usage, steps, nil
	}

	return nil, nil, steps, fmt.Errorf("tool-calling chain exceeded %d rounds without a final answer", maxChainRounds)
}
