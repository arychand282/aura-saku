package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (c *Client) ProviderName() string { return "anthropic" }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// converse sends the system prompt plus conversation messages and parses
// the response, retrying once with a repair instruction if the model's
// output isn't valid JSON or fails schema validation — same pattern as the
// Gemini and OpenAI clients.
func (c *Client) converse(ctx context.Context, systemPrompt string, messages []message) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	reqBody := messagesRequest{
		Model:     c.model,
		MaxTokens: 1024,
		System:    systemPrompt,
		Messages:  messages,
	}

	start := time.Now()
	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := tryParseMessages(resp)
	if !ok {
		reqBody.Messages = append(append([]message{}, messages...), message{
			Role:    "user",
			Content: "Your previous response was not valid JSON matching the required schema. Return ONLY the JSON object, no other text.",
		})
		resp, err = c.doRequest(ctx, reqBody)
		if err != nil {
			return nil, nil, err
		}
		parsed, ok = tryParseMessages(resp)
		if !ok {
			return &ai.ParsedTransaction{
				IsAmbiguous:           true,
				ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
			}, nil, nil
		}
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

func tryParseMessages(resp *messagesResponse) (*ai.ParsedTransaction, bool) {
	if len(resp.Content) == 0 {
		return nil, false
	}
	raw := ai.StripJSONFences(resp.Content[0].Text)
	var pt ai.ParsedTransaction
	if err := json.Unmarshal([]byte(raw), &pt); err != nil {
		return nil, false
	}
	if !ai.ValidateParsedTransaction(&pt) {
		return nil, false
	}
	return &pt, true
}

func (c *Client) doRequest(ctx context.Context, body messagesRequest) (*messagesResponse, error) {
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

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.BuildTransactionSystemPrompt(defaultCurrency, time.Now(), preferredLanguage)
	return c.converse(ctx, systemPrompt, []message{{Role: "user", Content: "User message: " + userText}})
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.BuildTransactionSystemPrompt(defaultCurrency, time.Now(), preferredLanguage)
	content := fmt.Sprintf(
		"Original user message: %s\nThe user was asked to clarify and replied: %s\nCombine both messages into a single transaction extraction.",
		originalText, clarificationReply,
	)
	return c.converse(ctx, systemPrompt, []message{{Role: "user", Content: content}})
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return nil, nil, apperror.ErrProviderAudioUnsupported
}
