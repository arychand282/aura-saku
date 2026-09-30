package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/prompt"
)

// defaultBaseURL is used when baseURL isn't overridden via config
// (GEMINI_BASE_URL) — override for a proxy, regional endpoint, or
// Vertex AI-compatible gateway.
const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/models"

// Client implements ai.AIClient against the Gemini REST API directly
// (no SDK dependency) using generateContent, including inline audio parts
// for native voice-note transcription+parsing in a single call.
type Client struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

// New builds a Client. An empty baseURL falls back to defaultBaseURL.
func New(apiKey, model, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		apiKey:     apiKey,
		model:      model,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

var _ ai.AIClient = (*Client)(nil)
var _ ai.ChainedCorrector = (*Client)(nil)

func (c *Client) ProviderName() string { return "gemini" }

type genPart struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *inlineData       `json:"inline_data,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	// ThoughtSignature must be echoed back verbatim on any functionCall part
	// replayed into history (see chain.go) — "thinking" models like
	// gemini-3.5-flash-lite reject a functionCall turn missing it with a
	// 400, discovered live rather than documented anywhere obvious.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

// functionResponse is how a tool's real result gets fed back to the model
// on the next round of a chained tool-calling loop — see chain.go. Only
// used by ParseCorrectionChained; the single-round-trip methods
// (ParseTransactionText etc.) never produce one of these.
type functionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type inlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

// genTool/functionDeclaration/functionSchema/propertySchema mirror the
// subset of Gemini's function-calling request schema this client uses —
// see internal/ai/gemini/tools.go for the actual tool declarations. This is
// the structured counterpart to the old approach's "describe the exact
// JSON shape in prose inside the prompt" — the schema is a real typed
// field on the request instead of a string the model has to reproduce
// faithfully.
type genTool struct {
	FunctionDeclarations []functionDeclaration `json:"functionDeclarations"`
}

type functionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  functionSchema `json:"parameters"`
}

type functionSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]propertySchema `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

type propertySchema struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// functionCall is what the model sends back instead of text when it
// decides to invoke one of the declared tools — Args is already a decoded
// key/value map (Gemini parses it against the declared schema on its
// side), not a string this client has to json.Unmarshal itself.
type functionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type genRequest struct {
	Contents          []genContent `json:"contents"`
	SystemInstruction *genContent  `json:"systemInstruction,omitempty"`
	Tools             []genTool    `json:"tools,omitempty"`
	GenerationConfig  struct {
		ResponseMimeType string `json:"responseMimeType,omitempty"`
	} `json:"generationConfig,omitempty"`
}

type genContent struct {
	Role  string    `json:"role"`
	Parts []genPart `json:"parts"`
}

type genResponse struct {
	Candidates []struct {
		Content struct {
			Parts []genPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// historyToContents converts prior conversation turns into leading Gemini
// contents entries, prepended before the current message — "assistant"
// turns map to Gemini's "model" role, everything else (only ever "user" in
// practice) passes through as "user".
func historyToContents(turns []ai.ConversationTurn) []genContent {
	contents := make([]genContent, 0, len(turns))
	for _, t := range turns {
		role := "user"
		if t.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, genContent{Role: role, Parts: []genPart{{Text: t.Content}}})
	}
	return contents
}

// generate sends the given content parts plus the declared transaction
// tools to Gemini and derives one ParsedTransaction per tool call the model
// decided to make — almost always one, but record_transaction may be
// called several times in a single turn for a message listing several
// distinct transactions (see internal/ai/prompt/tool_schema.go). Unlike the
// old prompt-engineered-JSON approach, there's no repair-retry loop here:
// the model either emits functionCalls whose args are already guaranteed by
// Gemini to match the declared schema, or it emits plain text — there's no
// third case of "text that was supposed to be JSON but isn't quite."
//
// history prepends prior conversation turns before parts and folds
// history.Summary into the system instruction, so a follow-up message
// (e.g. "totalnya 150rb" after "abis makan di restoran mahal tadi") is
// understood in context instead of parsed as if it were the first message
// ever sent. The zero value ConversationContext{} behaves exactly as this
// method did before history existed.
func (c *Client) generate(ctx context.Context, systemInstruction string, parts []genPart, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	contents := append(historyToContents(history.Recent), genContent{Role: "user", Parts: parts})
	reqBody := genRequest{
		Contents:          contents,
		SystemInstruction: &genContent{Parts: []genPart{{Text: systemInstruction + prompt.ConversationSummaryPreamble(history.Summary)}}},
		Tools:             transactionTools(),
	}

	start := time.Now()
	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := c.tryParseToolCalls(resp)
	if !ok {
		return []*ai.ParsedTransaction{{
			IsAmbiguous:           true,
			ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
		}}, nil, nil
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.UsageMetadata.PromptTokenCount,
		CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

// tryParseToolCalls maps every tool call the model made (if any) onto the
// shared ai.ParsedTransaction shape, so the rest of the app — bot.Service's
// intent switch, ai.ValidateParsedTransaction — is completely unaware this
// provider stopped hand-parsing JSON text. A response with no functionCall
// parts at all is the model choosing to reply in plain text instead (see
// toolCallingSystemInstruction) — that's this provider's "ambiguous" case,
// always a single-element result.
func (c *Client) tryParseToolCalls(resp *genResponse) ([]*ai.ParsedTransaction, bool) {
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, false
	}

	var results []*ai.ParsedTransaction
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.FunctionCall == nil {
			continue
		}
		pt := ai.ParsedTransactionFromToolCall(part.FunctionCall.Name, part.FunctionCall.Args)
		if pt == nil || !ai.ValidateParsedTransaction(pt) {
			return nil, false
		}
		results = append(results, pt)
	}
	if len(results) > 0 {
		return results, true
	}

	// No tool call in this response — the model chose to reply in plain
	// text instead (see toolCallingSystemInstruction's clarifying-question
	// instruction).
	text := resp.Candidates[0].Content.Parts[0].Text
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	return []*ai.ParsedTransaction{{IsAmbiguous: true, ClarificationQuestion: text}}, true
}

func (c *Client) doRequest(ctx context.Context, body genRequest) (*genResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:generateContent?key=%s", c.baseURL, c.model, c.apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build gemini request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("gemini request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gemini response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	var parsed genResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal gemini response: %w", err)
	}
	return &parsed, nil
}

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	instruction := toolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	parts := []genPart{{Text: "User message: " + userText}}
	return c.generate(ctx, instruction, parts, history)
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	instruction := toolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	parts := []genPart{
		{Text: "Transcribe the following voice note and extract the transaction it describes."},
		{InlineData: &inlineData{
			MimeType: mimeType,
			Data:     base64.StdEncoding.EncodeToString(audioBytes),
		}},
	}
	return c.generate(ctx, instruction, parts, history)
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	instruction := toolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	parts := []genPart{
		{Text: "Original user message: " + originalText},
		{Text: "The user was asked to clarify and replied: " + clarificationReply},
		{Text: "Combine both messages into a single transaction extraction."},
	}
	return c.generate(ctx, instruction, parts, history)
}

// Summarize sends priorSummary plus turns as a plain text-in/text-out
// completion (no tools declared — there's nothing to extract, only
// condense) and returns the model's updated summary text verbatim.
func (c *Client) Summarize(ctx context.Context, priorSummary string, turns []ai.ConversationTurn, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	lead := "Conversation so far (write a summary of it):"
	if priorSummary != "" {
		lead = "Prior summary: " + priorSummary + "\n\nNew messages since that summary (merge them into one updated summary):"
	}
	// lead must be the LAST content, not the first: Gemini rejects a
	// request ending on a "model" turn ("Requests ending with a model turn
	// are not supported"), and turns normally ends with the assistant's
	// reply to the most recent message — found live, not documented
	// anywhere obvious.
	contents := append(historyToContents(turns), genContent{Role: "user", Parts: []genPart{{Text: lead}}})

	start := time.Now()
	resp, err := c.doRequest(ctx, genRequest{
		Contents:          contents,
		SystemInstruction: &genContent{Parts: []genPart{{Text: prompt.SummarizationSystemInstruction(preferredLanguage)}}},
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", nil, fmt.Errorf("empty summarization response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.UsageMetadata.PromptTokenCount,
		CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Candidates[0].Content.Parts[0].Text), usage, nil
}

// SuggestSavings is Summarize's sibling: same plain text-in/text-out shape
// (no tools, no history needed), just a different system instruction and a
// single spendingSummary message instead of a conversation to condense.
func (c *Client) SuggestSavings(ctx context.Context, spendingSummary string, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	start := time.Now()
	resp, err := c.doRequest(ctx, genRequest{
		Contents:          []genContent{{Role: "user", Parts: []genPart{{Text: spendingSummary}}}},
		SystemInstruction: &genContent{Parts: []genPart{{Text: prompt.SavingsSuggestionSystemInstruction(preferredLanguage)}}},
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", nil, fmt.Errorf("empty savings suggestion response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.UsageMetadata.PromptTokenCount,
		CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Candidates[0].Content.Parts[0].Text), usage, nil
}
