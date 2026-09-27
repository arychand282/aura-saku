package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
		Name:        toolGetLastTransaction,
		Description: "Call this BEFORE correct_last_transaction if the correction message doesn't restate every field of the original transaction (e.g. \"eh salah, harusnya 1 juta aja\" only gives a corrected amount, not a category) — use its result to fill in whatever correct_last_transaction still needs. Takes no parameters. Skip this entirely if the correction message already states type/amount/currency/category on its own.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]propertySchema{}},
	})
}

// chainMessage is chain.go's own message type, distinct from converse()'s
// plain message (Content: string) — replaying a tool_use/tool_result turn
// needs Content to hold an array of content blocks instead of plain text,
// and Go's JSON encoding can't express "string or array" on one field
// without falling back to interface{}.
type chainMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// toolUseBlock replays the model's own tool_use block back into history
// verbatim. Input deliberately has NO omitempty: Anthropic's real API
// always includes "input" on a tool_use block, even as {} for a
// zero-argument tool like get_last_transaction — omitempty would silently
// drop it for exactly that case, since Go treats a 0-length map as empty
// regardless of nil-ness.
type toolUseBlock struct {
	Type  string                 `json:"type"`
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

// toolResultBlock carries the tool's real answer back — a distinct type
// from toolUseBlock (not the same struct reused with different fields
// populated) so neither one risks leaking the other's fields, e.g. a
// stray "input": null on a result block.
type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

type chainRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system"`
	Messages  []chainMessage `json:"messages"`
	Tools     []toolDef      `json:"tools,omitempty"`
}

// ParseCorrectionChained is Anthropic's counterpart to
// gemini.Client.ParseCorrectionChained — same mechanism (a loop resending
// an ever-growing message list, no special "continue" API), different wire
// shape: the model's tool-call turn is replayed back as an assistant
// message with a tool_use content block, and the tool's result comes back
// as a user message with a tool_result block correlated by ToolUseID
// (Anthropic's equivalent of OpenAI's tool_call_id).
func (c *Client) ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, lookup ai.LastTransactionLookup) (*ai.ParsedTransaction, *ai.UsageMetadata, []ai.ChainStep, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	tools := correctionChainTools()

	messages := []chainMessage{
		{Role: "user", Content: "User message: " + userText},
	}

	var steps []ai.ChainStep
	usage := &ai.UsageMetadata{Model: c.model}
	start := time.Now()

	for round := 0; round < maxChainRounds; round++ {
		resp, err := c.doRequest(ctx, chainRequest{
			Model:     c.model,
			MaxTokens: 1024,
			System:    systemPrompt,
			Messages:  messages,
			Tools:     tools,
		})
		if err != nil {
			return nil, nil, steps, err
		}
		usage.PromptTokens += resp.Usage.InputTokens
		usage.CompletionTokens += resp.Usage.OutputTokens

		if len(resp.Content) == 0 {
			break
		}

		toolBlock := findToolUseBlock(resp.Content)
		if toolBlock == nil {
			text := resp.Content[0].Text
			usage.LatencyMs = int(time.Since(start).Milliseconds())
			if strings.TrimSpace(text) == "" {
				return nil, nil, steps, fmt.Errorf("empty response with no tool_use block")
			}
			return &ai.ParsedTransaction{IsAmbiguous: true, ClarificationQuestion: text}, usage, steps, nil
		}

		if toolBlock.Name == toolGetLastTransaction {
			result, lookupErr := lookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_last_transaction lookup: %w", lookupErr)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: toolBlock.Name, Args: toolBlock.Input, Result: result})

			resultJSON, jsonErr := json.Marshal(ai.LastTransactionToMap(result))
			if jsonErr != nil {
				return nil, nil, steps, fmt.Errorf("marshal tool result: %w", jsonErr)
			}
			input := toolBlock.Input
			if input == nil {
				input = map[string]interface{}{}
			}
			messages = append(messages,
				chainMessage{Role: "assistant", Content: []toolUseBlock{
					{Type: "tool_use", ID: toolBlock.ID, Name: toolBlock.Name, Input: input},
				}},
				chainMessage{Role: "user", Content: []toolResultBlock{
					{Type: "tool_result", ToolUseID: toolBlock.ID, Content: string(resultJSON)},
				}},
			)
			continue
		}

		steps = append(steps, ai.ChainStep{ToolCalled: toolBlock.Name, Args: toolBlock.Input})
		pt := ai.ParsedTransactionFromToolCall(toolBlock.Name, toolBlock.Input)
		if pt == nil || !ai.ValidateParsedTransaction(pt) {
			return nil, nil, steps, fmt.Errorf("model called unrecognized/invalid tool %q", toolBlock.Name)
		}
		usage.LatencyMs = int(time.Since(start).Milliseconds())
		return pt, usage, steps, nil
	}

	return nil, nil, steps, fmt.Errorf("tool-calling chain exceeded %d rounds without a final answer", maxChainRounds)
}

func findToolUseBlock(blocks []responseContentBlock) *responseContentBlock {
	for i := range blocks {
		if blocks[i].Type == "tool_use" {
			return &blocks[i]
		}
	}
	return nil
}
