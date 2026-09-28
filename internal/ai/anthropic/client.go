package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/prompt"
	"aura-saku/internal/apperror"
)

// defaultBaseURL is used when baseURL isn't overridden via config
// (ANTHROPIC_BASE_URL) — override for a Bedrock/Vertex-hosted Claude
// endpoint or a corporate proxy.
const defaultBaseURL = "https://api.anthropic.com/v1"

// defaultAPIVersion is used when apiVersion isn't overridden via config
// (ANTHROPIC_API_VERSION). Note this only covers a version-header bump —
// if Anthropic ever changes the actual request/response JSON shape, the
// messagesRequest/messagesResponse structs below would need a code change
// regardless of this override.
const defaultAPIVersion = "2023-06-01"

// Client implements ai.AIClient against the Anthropic Messages API
// directly (no SDK dependency). Claude has no native audio transcription,
// so TranscribeAndParseAudio returns apperror.ErrProviderAudioUnsupported —
// callers should either switch AI_PROVIDER or add a dedicated STT step.
type Client struct {
	apiKey     string
	model      string
	baseURL    string
	apiVersion string
	httpClient *http.Client
}

// New builds a Client. An empty baseURL/apiVersion falls back to this
// package's defaults.
func New(apiKey, model, baseURL, apiVersion string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	if apiVersion == "" {
		apiVersion = defaultAPIVersion
	}
	return &Client{
		apiKey:     apiKey,
		model:      model,
		baseURL:    baseURL,
		apiVersion: apiVersion,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

var _ ai.AIClient = (*Client)(nil)
var _ ai.ChainedCorrector = (*Client)(nil)

func (c *Client) ProviderName() string { return "anthropic" }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// toolDef/inputSchema/propertySchema mirror the subset of Anthropic's
// function-calling request schema this client uses — see
// internal/ai/anthropic/tools.go for the actual tool declarations, adapted
// from the same prompt.TransactionTools() the gemini and openai providers
// also use. Anthropic declares tools at the top level (name/description/
// input_schema), unlike OpenAI's nested {type, function: {...}}.
type toolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"input_schema"`
}

type inputSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]propertySchema `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

type propertySchema struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
	Tools     []toolDef `json:"tools,omitempty"`
}

// A content block is either type "text" (plain reply) or type "tool_use"
// (the model invoking one of the declared tools) — Input is already a
// decoded object on this one, same as Gemini's args and unlike OpenAI's
// JSON-encoded-string arguments. Named (rather than anonymous) so chain.go
// can build a []responseContentBlock replaying a tool_use block verbatim.
type responseContentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

type messagesResponse struct {
	Content []responseContentBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// historyToMessages converts prior conversation turns into leading
// messages, prepended before the current turn(s). ai.ConversationTurn.Role
// ("user"/"assistant") already matches Anthropic's own role vocabulary —
// no translation needed, unlike Gemini's "model".
func historyToMessages(turns []ai.ConversationTurn) []message {
	msgs := make([]message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, message{Role: t.Role, Content: t.Content})
	}
	return msgs
}

// converse sends the system prompt plus history plus conversation messages
// plus the declared transaction tools and derives one ParsedTransaction per
// tool call the model decided to make — almost always one, but
// record_transaction may be called several times in a single turn for a
// message listing several distinct transactions (see
// internal/ai/prompt/tool_schema.go). No repair-retry loop: a tool_use
// block's Input is schema-guaranteed by Anthropic to match the declared
// input_schema, and content with no tool_use block is the model choosing
// to reply in plain text instead (this provider's "ambiguous" case) —
// there's no third case of "text that was supposed to be JSON but isn't
// quite."
//
// history prepends prior conversation turns before currentMessages and
// folds history.Summary into the system prompt, so a follow-up message is
// understood in context instead of parsed as if it were the first message
// ever sent. The zero value ConversationContext{} behaves exactly as this
// method did before history existed.
func (c *Client) converse(ctx context.Context, systemPrompt string, currentMessages []message, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	messages := append(historyToMessages(history.Recent), currentMessages...)
	reqBody := messagesRequest{
		Model:     c.model,
		MaxTokens: 1024,
		System:    systemPrompt + prompt.ConversationSummaryPreamble(history.Summary),
		Messages:  messages,
		Tools:     transactionTools(),
	}

	start := time.Now()
	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := tryParseToolUses(resp)
	if !ok {
		return []*ai.ParsedTransaction{{
			IsAmbiguous:           true,
			ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
		}}, nil, nil
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

func tryParseToolUses(resp *messagesResponse) ([]*ai.ParsedTransaction, bool) {
	if len(resp.Content) == 0 {
		return nil, false
	}

	var results []*ai.ParsedTransaction
	for _, block := range resp.Content {
		if block.Type != "tool_use" {
			continue
		}
		pt := ai.ParsedTransactionFromToolCall(block.Name, block.Input)
		if pt == nil || !ai.ValidateParsedTransaction(pt) {
			return nil, false
		}
		results = append(results, pt)
	}
	if len(results) > 0 {
		return results, true
	}

	// No tool_use block in this response — the model chose to reply in
	// plain text instead.
	text := resp.Content[0].Text
	if strings.TrimSpace(text) == "" {
		return nil, false
	}
	return []*ai.ParsedTransaction{{IsAmbiguous: true, ClarificationQuestion: text}}, true
}

// doRequest accepts any (both messagesRequest and chain.go's chainRequest —
// same "/messages" endpoint, different message-array element shape)
// rather than being generic-typed, since only json.Marshal ever touches
// body here.
func (c *Client) doRequest(ctx context.Context, body any) (*messagesResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal anthropic request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build anthropic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", c.apiVersion)

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read anthropic response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	var parsed messagesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal anthropic response: %w", err)
	}
	return &parsed, nil
}

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	return c.converse(ctx, systemPrompt, []message{{Role: "user", Content: "User message: " + userText}}, history)
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	content := fmt.Sprintf(
		"Original user message: %s\nThe user was asked to clarify and replied: %s\nCombine both messages into a single transaction extraction.",
		originalText, clarificationReply,
	)
	return c.converse(ctx, systemPrompt, []message{{Role: "user", Content: content}}, history)
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrProviderAudioUnsupported
}

// Summarize sends priorSummary plus turns as a plain text-in/text-out
// completion (no tools declared — there's nothing to extract, only
// condense) and returns the model's updated summary text verbatim.
func (c *Client) Summarize(ctx context.Context, priorSummary string, turns []ai.ConversationTurn, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	lead := "Conversation so far (write a summary of it):"
	if priorSummary != "" {
		lead = "Prior summary: " + priorSummary + "\n\nNew messages since that summary (merge them into one updated summary):"
	}
	messages := append(historyToMessages(turns), message{Role: "user", Content: lead})
	// lead must be the LAST message (Anthropic requires the final message
	// to be role "user"), so put it after the turns rather than before.

	start := time.Now()
	resp, err := c.doRequest(ctx, messagesRequest{
		Model:     c.model,
		MaxTokens: 1024,
		System:    prompt.SummarizationSystemInstruction(preferredLanguage),
		Messages:  messages,
	})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Content) == 0 || strings.TrimSpace(resp.Content[0].Text) == "" {
		return "", nil, fmt.Errorf("empty summarization response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Content[0].Text), usage, nil
}
