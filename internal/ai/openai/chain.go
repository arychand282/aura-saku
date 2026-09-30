package openai

import (
	"context"
	"encoding/json"
	"fmt"
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
			Type: "function",
			Function: functionDef{
				Name:        toolGetRecentTransactions,
				Description: "Call this BEFORE correct_transaction whenever the transaction being fixed might not be the single most recent one — e.g. several transactions were just recorded from one message, or the correction refers to something further back by category/amount/description — or when the correction message doesn't restate every field of the original (e.g. \"eh salah, harusnya 1 juta aja\" only gives a corrected amount, not a category). Returns the user's recent transactions, each with an id — pass the matching one's id back as correct_transaction's target_transaction_id, and use its other fields to fill in whatever correct_transaction still needs. Takes no parameters. Skip this only when it's obviously the single most recent transaction AND the correction message already states type/amount/currency/category on its own.",
				Parameters:  functionParameters{Type: "object", Properties: map[string]propertySchema{}},
			},
		},
		toolDef{
			Type: "function",
			Function: functionDef{
				Name:        toolGetRecurringTransactions,
				Description: "Call this BEFORE delete_recurring_transaction to find the id of the recurring transaction/bill reminder the message wants canceled. Returns the user's active recurring transactions and bill reminders, each with an id, type, amount, category, description, day_of_month, and whether it's reminder_only — match the message against these by category/amount/description. Takes no parameters. Call this even when there's only one active one, so the id used is real rather than guessed.",
				Parameters:  functionParameters{Type: "object", Properties: map[string]propertySchema{}},
			},
		},
	)
}

// ParseCorrectionChained is OpenAI's counterpart to
// gemini.Client.ParseCorrectionChained — same mechanism (a loop resending
// an ever-growing message list, no special "continue" API), different wire
// shape: the model's tool-call turn is replayed back as an assistant
// message carrying ToolCalls, and the tool's result is a "tool"-role
// message keyed by ToolCallID rather than a name-keyed functionResponse
// part like Gemini's. Like every other method, the terminal round can
// yield more than one ParsedTransaction — record_transaction may be called
// several times for a message listing several distinct transactions (see
// internal/ai/prompt/tool_schema.go), which OpenAI represents as several
// entries in the same msg.ToolCalls; get_recent_transactions never mixes
// with that, so a chaining round is detected as "any
// get_recent_transactions call present" and everything else in that round
// is deferred to the next.
func (c *Client) ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, txLookup ai.RecentTransactionsLookup, recurringLookup ai.RecurringTransactionsLookup, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, []ai.ChainStep, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage) + prompt.ConversationSummaryPreamble(history.Summary)
	tools := correctionChainTools()

	messages := append([]chatMessage{{Role: "system", Content: systemPrompt}}, historyToMessages(history.Recent)...)
	messages = append(messages, chatMessage{Role: "user", Content: "User message: " + userText})

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
			return []*ai.ParsedTransaction{{IsAmbiguous: true, ClarificationQuestion: msg.Content}}, usage, steps, nil
		}

		if lookupTC := findToolCallByName(msg.ToolCalls, toolGetRecentTransactions); lookupTC != nil {
			var lookupArgs map[string]interface{}
			if err := json.Unmarshal([]byte(lookupTC.Function.Arguments), &lookupArgs); err != nil {
				return nil, nil, steps, fmt.Errorf("unmarshal tool call arguments: %w", err)
			}
			results, lookupErr := txLookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_recent_transactions lookup: %w", lookupErr)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: lookupTC.Function.Name, Args: lookupArgs, Results: results})

			resultJSON, jsonErr := json.Marshal(ai.RecentTransactionsToMap(results))
			if jsonErr != nil {
				return nil, nil, steps, fmt.Errorf("marshal tool result: %w", jsonErr)
			}
			// Replay the model's own tool-call turn (ToolCalls, no Content)
			// then the real result keyed by ToolCallID — OpenAI correlates
			// a tool message to its call by this ID, not by name/position.
			messages = append(messages,
				chatMessage{Role: "assistant", ToolCalls: msg.ToolCalls},
				chatMessage{Role: "tool", ToolCallID: lookupTC.ID, Content: string(resultJSON)},
			)
			continue
		}

		if lookupTC := findToolCallByName(msg.ToolCalls, toolGetRecurringTransactions); lookupTC != nil {
			var lookupArgs map[string]interface{}
			if err := json.Unmarshal([]byte(lookupTC.Function.Arguments), &lookupArgs); err != nil {
				return nil, nil, steps, fmt.Errorf("unmarshal tool call arguments: %w", err)
			}
			results, lookupErr := recurringLookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_recurring_transactions lookup: %w", lookupErr)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: lookupTC.Function.Name, Args: lookupArgs, RecurringResults: results})

			resultJSON, jsonErr := json.Marshal(ai.RecurringTransactionsToMap(results))
			if jsonErr != nil {
				return nil, nil, steps, fmt.Errorf("marshal tool result: %w", jsonErr)
			}
			messages = append(messages,
				chatMessage{Role: "assistant", ToolCalls: msg.ToolCalls},
				chatMessage{Role: "tool", ToolCallID: lookupTC.ID, Content: string(resultJSON)},
			)
			continue
		}

		// Terminal round — one or more non-lookup tool calls.
		results := make([]*ai.ParsedTransaction, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, nil, steps, fmt.Errorf("unmarshal tool call arguments: %w", err)
			}
			steps = append(steps, ai.ChainStep{ToolCalled: tc.Function.Name, Args: args})
			pt := ai.ParsedTransactionFromToolCall(tc.Function.Name, args)
			if pt == nil || !ai.ValidateParsedTransaction(pt) {
				return nil, nil, steps, fmt.Errorf("model called unrecognized/invalid tool %q", tc.Function.Name)
			}
			results = append(results, pt)
		}
		usage.LatencyMs = int(time.Since(start).Milliseconds())
		return results, usage, steps, nil
	}

	return nil, nil, steps, fmt.Errorf("tool-calling chain exceeded %d rounds without a final answer", maxChainRounds)
}

func findToolCallByName(calls []toolCall, name string) *toolCall {
	for i := range calls {
		if calls[i].Function.Name == name {
			return &calls[i]
		}
	}
	return nil
}
