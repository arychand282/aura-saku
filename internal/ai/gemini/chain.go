package gemini

import (
	"context"
	"fmt"
	"time"

	"aura-saku/internal/ai"
)

const toolGetLastTransaction = "get_last_transaction"

// maxChainRounds bounds how many times ParseCorrectionChained will call the
// API in a single request — a model that somehow kept calling tools
// forever would otherwise turn one user message into unbounded real API
// calls. There's no legitimate reason this specific chain needs more than
// 2 rounds (get_last_transaction, then a terminal tool); 4 leaves headroom
// without being effectively unbounded.
const maxChainRounds = 4

// LastTransaction is what get_last_transaction hands back to the model —
// mirrors the fields transaction.Service.MostRecent's result exposes,
// without this package importing that DB-facing service directly (this
// package stays a leaf dependency, same as every other provider client —
// see ai.AIClient's own doc comment).
type LastTransaction struct {
	Found        bool
	Type         string
	Amount       float64
	Currency     string
	CategoryName string
	Bucket       string
	Description  string
	OccurredAt   string // YYYY-MM-DD
}

// LastTransactionLookup executes the real get_last_transaction tool — wire
// in transaction.Service.MostRecent here in production. Called for real,
// but only when (and only when) the model actually asks for it; a
// correction message that already restates every field never triggers it.
type LastTransactionLookup func(ctx context.Context) (*LastTransaction, error)

// ChainStep records one round of a chained call — which tool the model
// asked for, with what arguments, and (for get_last_transaction) what real
// result it got back — so a caller can inspect or print exactly what
// happened. See cmd/agenticdemo for a runnable example against the real
// API.
type ChainStep struct {
	ToolCalled string
	Args       map[string]interface{}
	Result     *LastTransaction
}

// correctionChainTools is the shared 4 tools (transactionTools) plus a 5th
// one only this method offers: get_last_transaction. Deliberately NOT
// added to the shared prompt.TransactionTools() list every provider uses —
// OpenAI's and Anthropic's clients are still single-round-trip and have no
// loop to satisfy a tool call with, so advertising this tool to them would
// let the model call something those clients can never actually fulfill.
func correctionChainTools() []genTool {
	tools := transactionTools()
	tools[0].FunctionDeclarations = append(tools[0].FunctionDeclarations, functionDeclaration{
		Name:        toolGetLastTransaction,
		Description: "Call this BEFORE correct_last_transaction if the correction message doesn't restate every field of the original transaction (e.g. \"eh salah, harusnya 1 juta aja\" only gives a corrected amount, not a category) — use its result to fill in whatever correct_last_transaction still needs. Takes no parameters. Skip this entirely if the correction message already states type/amount/currency/category on its own.",
		Parameters:  functionSchema{Type: "OBJECT", Properties: map[string]propertySchema{}},
	})
	return tools
}

func lastTransactionToResponseMap(lt *LastTransaction) map[string]interface{} {
	if lt == nil || !lt.Found {
		return map[string]interface{}{"found": false}
	}
	return map[string]interface{}{
		"found":         true,
		"type":          lt.Type,
		"amount":        lt.Amount,
		"currency":      lt.Currency,
		"category_name": lt.CategoryName,
		"bucket":        lt.Bucket,
		"description":   lt.Description,
		"occurred_at":   lt.OccurredAt,
	}
}

// ParseCorrectionChained demonstrates real multi-step tool calling, unlike
// every other method on this client (all single round trip): the model is
// offered a second tool, get_last_transaction, that it can call BEFORE
// deciding how to fill in correct_last_transaction. Whether it actually
// chains is still the model's own judgement call each time — a correction
// that restates every field needs no lookup at all; a partial one does.
//
// The mechanism is exactly what it looks like at the wire level: there is
// no special "continue" API. Each round re-sends the growing conversation
// — the model's own tool-call turn plus the tool's real result appended as
// a new turn — and the model decides fresh, every round, whether it has
// enough now or needs to call something else.
func (c *Client) ParseCorrectionChained(ctx context.Context, userText, defaultCurrency, preferredLanguage string, lookup LastTransactionLookup) (*ai.ParsedTransaction, *ai.UsageMetadata, []ChainStep, error) {
	instruction := toolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	tools := correctionChainTools()

	contents := []genContent{{Role: "user", Parts: []genPart{{Text: "User message: " + userText}}}}

	var steps []ChainStep
	usage := &ai.UsageMetadata{Model: c.model}
	start := time.Now()

	for round := 0; round < maxChainRounds; round++ {
		resp, err := c.doRequest(ctx, genRequest{
			Contents:          contents,
			SystemInstruction: &genContent{Parts: []genPart{{Text: instruction}}},
			Tools:             tools,
		})
		if err != nil {
			return nil, nil, steps, err
		}
		usage.PromptTokens += resp.UsageMetadata.PromptTokenCount
		usage.CompletionTokens += resp.UsageMetadata.CandidatesTokenCount

		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			break
		}

		callPart := findFunctionCallPart(resp.Candidates[0].Content.Parts)
		if callPart == nil {
			// No tool call in this round — the model replied in plain text
			// instead (this chain's "ambiguous" case, same convention as
			// every other method on this client).
			text := resp.Candidates[0].Content.Parts[0].Text
			usage.LatencyMs = int(time.Since(start).Milliseconds())
			return &ai.ParsedTransaction{IsAmbiguous: true, ClarificationQuestion: text}, usage, steps, nil
		}
		fc := callPart.FunctionCall

		if fc.Name == toolGetLastTransaction {
			result, lookupErr := lookup(ctx)
			if lookupErr != nil {
				return nil, nil, steps, fmt.Errorf("get_last_transaction lookup: %w", lookupErr)
			}
			steps = append(steps, ChainStep{ToolCalled: fc.Name, Args: fc.Args, Result: result})

			// Replay the model's turn EXACTLY as received (including
			// ThoughtSignature, not just the FunctionCall) — reconstructing
			// a bare {FunctionCall: fc} here is what triggered the 400
			// above; Gemini demands the signature back verbatim.
			contents = append(contents,
				genContent{Role: "model", Parts: []genPart{*callPart}},
				genContent{Role: "user", Parts: []genPart{{FunctionResponse: &functionResponse{
					Name:     fc.Name,
					Response: lastTransactionToResponseMap(result),
				}}}},
			)
			continue
		}

		// A terminal tool — same mapping every other method on this client
		// uses once it has a functionCall in hand.
		steps = append(steps, ChainStep{ToolCalled: fc.Name, Args: fc.Args})
		pt := ai.ParsedTransactionFromToolCall(fc.Name, fc.Args)
		if pt == nil || !ai.ValidateParsedTransaction(pt) {
			return nil, nil, steps, fmt.Errorf("model called unrecognized/invalid tool %q", fc.Name)
		}
		usage.LatencyMs = int(time.Since(start).Milliseconds())
		return pt, usage, steps, nil
	}

	return nil, nil, steps, fmt.Errorf("tool-calling chain exceeded %d rounds without a final answer", maxChainRounds)
}

func findFunctionCallPart(parts []genPart) *genPart {
	for i := range parts {
		if parts[i].FunctionCall != nil {
			return &parts[i]
		}
	}
	return nil
}
