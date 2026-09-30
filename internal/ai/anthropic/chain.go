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

const toolGetRecentTransactions = "get_recent_transactions"
const toolGetRecurringTransactions = "get_recurring_transactions"

// maxChainRounds bounds how many times ParseCorrectionChained will call the
// API in a single request — see the identical constant/comment on
// gemini.Client.ParseCorrectionChained.
const maxChainRounds = 4

// correctionChainTools is the shared tools (transactionTools) plus two more
// only this method offers: get_recent_transactions and
// get_recurring_transactions. Deliberately NOT added to the shared
// prompt.TransactionTools() list every provider uses — see
// gemini/chain.go's identical comment for why.
func correctionChainTools() []toolDef {
	tools := transactionTools()
	return append(tools,
		toolDef{
			Name:        toolGetRecentTransactions,
			Description: "Call this BEFORE correct_transaction whenever the transaction being fixed might not be the single most recent one — e.g. several transactions were just recorded from one message, or the correction refers to something further back by category/amount/description — or when the correction message doesn't restate every field of the original (e.g. \"eh salah, harusnya 1 juta aja\" only gives a corrected amount, not a category). Returns the user's recent transactions, each with an id — pass the matching one's id back as correct_transaction's target_transaction_id, and use its other fields to fill in whatever correct_transaction still needs. Takes no parameters. Skip this only when it's obviously the single most recent transaction AND the correction message already states type/amount/currency/category on its own.",
			InputSchema: inputSchema{Type: "object", Properties: map[string]propertySchema{}},
		},
		toolDef{
			Name:        toolGetRecurringTransactions,
			Description: "Call this BEFORE delete_recurring_transaction to find the id of the recurring transaction/bill reminder the message wants canceled. Returns the user's active recurring transactions and bill reminders, each with an id, type, amount, category, description, day_of_month, and whether it's reminder_only — match the message against these by category/amount/description. Takes no parameters. Call this even when there's only one active one, so the id used is real rather than guessed.",
			InputSchema: inputSchema{Type: "object", Properties: map[string]propertySchema{}},
		},
	)
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
// zero-argument tool like get_recent_transactions — omitempty would
// silently drop it for exactly that case, since Go treats a 0-length map as
// empty regardless of nil-ness.
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
// (Anthropic's equivalent of OpenAI's tool_call_id). Like every other
// method, the terminal round can yield more than one ParsedTransaction —
// record_transaction may be called several times for a message listing
// several distinct transactions (see internal/ai/prompt/tool_schema.go),
// which Anthropic represents as several tool_use blocks in the same
// response; get_recent_transactions never mixes with that, so a chaining
// round is detected as "any get_recent_transactions block present" and
// everything else in that round is deferred to the next.
func (c *Client) ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, txLookup ai.RecentTransactionsLookup, recurringLookup ai.RecurringTransactionsLookup, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, []ai.ChainStep, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage) + prompt.ConversationSummaryPreamble(history.Summary)
	tools := correctionChainTools()

	messages := make([]chainMessage, 0, len(history.Recent)+1)
	for _, t := range history.Recent {
		messages = append(messages, chainMessage{Role: t.Role, Content: t.Content})
	}
	messages = append(messages, chainMessage{Role: "user", Content: "User message: " + userText})

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

		toolBlocks := findToolUseBlocks(resp.Content)
		if len(toolBlocks) == 0 {
			text := resp.Content[0].Text
			usage.LatencyMs = int(time.Since(start).Milliseconds())
			if strings.TrimSpace(text) == "" {
				return nil, nil, steps, fmt.Errorf("empty response with no tool_use block")
			}
			return []*ai.ParsedTransaction{{IsAmbiguous: true, ClarificationQuestion: text}}, usage, steps, nil
		}

		if lookupBlock := findBlockByName(toolBlocks, toolGetRecentTransactions); lookupBlock != nil {
			results, lookupErr := txLookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_recent_transactions lookup: %w", lookupErr)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: lookupBlock.Name, Args: lookupBlock.Input, Results: results})

			resultJSON, jsonErr := json.Marshal(ai.RecentTransactionsToMap(results))
			if jsonErr != nil {
				return nil, nil, steps, fmt.Errorf("marshal tool result: %w", jsonErr)
			}
			input := lookupBlock.Input
			if input == nil {
				input = map[string]interface{}{}
			}
			messages = append(messages,
				chainMessage{Role: "assistant", Content: []toolUseBlock{
					{Type: "tool_use", ID: lookupBlock.ID, Name: lookupBlock.Name, Input: input},
				}},
				chainMessage{Role: "user", Content: []toolResultBlock{
					{Type: "tool_result", ToolUseID: lookupBlock.ID, Content: string(resultJSON)},
				}},
			)
			continue
		}

		if lookupBlock := findBlockByName(toolBlocks, toolGetRecurringTransactions); lookupBlock != nil {
			results, lookupErr := recurringLookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_recurring_transactions lookup: %w", lookupErr)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: lookupBlock.Name, Args: lookupBlock.Input, RecurringResults: results})

			resultJSON, jsonErr := json.Marshal(ai.RecurringTransactionsToMap(results))
			if jsonErr != nil {
				return nil, nil, steps, fmt.Errorf("marshal tool result: %w", jsonErr)
			}
			input := lookupBlock.Input
			if input == nil {
				input = map[string]interface{}{}
			}
			messages = append(messages,
				chainMessage{Role: "assistant", Content: []toolUseBlock{
					{Type: "tool_use", ID: lookupBlock.ID, Name: lookupBlock.Name, Input: input},
				}},
				chainMessage{Role: "user", Content: []toolResultBlock{
					{Type: "tool_result", ToolUseID: lookupBlock.ID, Content: string(resultJSON)},
				}},
			)
			continue
		}

		// Terminal round — one or more non-lookup tool_use blocks.
		results := make([]*ai.ParsedTransaction, 0, len(toolBlocks))
		for _, block := range toolBlocks {
			steps = append(steps, ai.ChainStep{ToolCalled: block.Name, Args: block.Input})
			pt := ai.ParsedTransactionFromToolCall(block.Name, block.Input)
			if pt == nil || !ai.ValidateParsedTransaction(pt) {
				return nil, nil, steps, fmt.Errorf("model called unrecognized/invalid tool %q", block.Name)
			}
			results = append(results, pt)
		}
		usage.LatencyMs = int(time.Since(start).Milliseconds())
		return results, usage, steps, nil
	}

	return nil, nil, steps, fmt.Errorf("tool-calling chain exceeded %d rounds without a final answer", maxChainRounds)
}

func findToolUseBlocks(blocks []responseContentBlock) []*responseContentBlock {
	var found []*responseContentBlock
	for i := range blocks {
		if blocks[i].Type == "tool_use" {
			found = append(found, &blocks[i])
		}
	}
	return found
}

func findBlockByName(blocks []*responseContentBlock, name string) *responseContentBlock {
	for _, b := range blocks {
		if b.Name == name {
			return b
		}
	}
	return nil
}
