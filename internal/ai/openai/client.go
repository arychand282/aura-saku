package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/prompt"
)

// defaultBaseURL is used when baseURL isn't overridden via config
// (OPENAI_BASE_URL) — override for Azure OpenAI, a self-hosted
// OpenAI-compatible gateway (LiteLLM, vLLM, Ollama), or a corporate proxy.
const defaultBaseURL = "https://api.openai.com/v1"

// Client implements ai.AIClient against the OpenAI REST API directly (no
// SDK dependency): Chat Completions with JSON-object response format for
// text parsing, and Whisper for voice-note transcription before feeding the
// transcript through the same text-parsing path.
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

func (c *Client) ProviderName() string { return "openai" }

// chatMessage covers every role this client sends: system/user (Role +
// Content), an assistant turn replaying a prior tool call (Role +
// ToolCalls, no Content), and a tool-result turn (Role: "tool" +
// ToolCallID + Content) — see chain.go for the only place that last shape
// is used.
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// toolDef/functionDef/functionParameters/propertySchema mirror the subset
// of OpenAI's function-calling request schema this client uses — see
// internal/ai/openai/tools.go for the actual tool declarations, adapted
// from the same prompt.TransactionTools() the gemini and anthropic
// providers also use.
type toolDef struct {
	Type     string      `json:"type"`
	Function functionDef `json:"function"`
}

type functionDef struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Parameters  functionParameters `json:"parameters"`
}

type functionParameters struct {
	Type       string                    `json:"type"`
	Properties map[string]propertySchema `json:"properties"`
	Required   []string                  `json:"required,omitempty"`
}

type propertySchema struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// toolCall is what a choice's message carries instead of (or alongside)
// Content when the model decides to invoke one of the declared tools.
// Unlike Gemini's args (already a decoded map) or Anthropic's input (same),
// OpenAI's Arguments comes back as a JSON-encoded STRING that still needs
// one json.Unmarshal — but that string is schema-guaranteed to parse and
// match the declared shape, unlike the old approach's "hope the whole
// message body happens to be valid JSON."
type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []toolDef     `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// chat sends a system+user message pair plus the declared transaction
// tools and derives a ParsedTransaction from whichever tool (if any) the
// model decided to call. No repair-retry loop: a tool_call's Arguments are
// schema-guaranteed by OpenAI to match the declared parameters, and a
// message with no tool_calls is the model choosing to reply in plain text
// instead (this provider's "ambiguous" case) — there's no third case of
// "text that was supposed to be JSON but isn't quite."
func (c *Client) chat(ctx context.Context, systemPrompt, userContent string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		Tools: transactionTools(),
	}

	start := time.Now()
	resp, err := c.doChatRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := tryParseToolCall(resp)
	if !ok {
		return &ai.ParsedTransaction{
			IsAmbiguous:           true,
			ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
		}, nil, nil
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

func tryParseToolCall(resp *chatResponse) (*ai.ParsedTransaction, bool) {
	if len(resp.Choices) == 0 {
		return nil, false
	}
	msg := resp.Choices[0].Message

	if len(msg.ToolCalls) > 0 {
		tc := msg.ToolCalls[0]
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return nil, false
		}
		pt := ai.ParsedTransactionFromToolCall(tc.Function.Name, args)
		if pt == nil || !ai.ValidateParsedTransaction(pt) {
			return nil, false
		}
		return pt, true
	}

	if msg.Content == "" {
		return nil, false
	}
	return &ai.ParsedTransaction{IsAmbiguous: true, ClarificationQuestion: msg.Content}, true
}

func (c *Client) doChatRequest(ctx context.Context, body chatRequest) (*chatResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build openai request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request failed: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read openai response: %w", err)
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai API error (status %d): %s", httpResp.StatusCode, string(respBody))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal openai response: %w", err)
	}
	return &parsed, nil
}

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	return c.chat(ctx, systemPrompt, "User message: "+userText)
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	userContent := fmt.Sprintf(
		"Original user message: %s\nThe user was asked to clarify and replied: %s\nCombine both messages into a single transaction extraction.",
		originalText, clarificationReply,
	)
	return c.chat(ctx, systemPrompt, userContent)
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	transcript, err := c.transcribe(ctx, audioBytes, mimeType)
	if err != nil {
		return nil, nil, fmt.Errorf("transcribe audio: %w", err)
	}
	return c.ParseTransactionText(ctx, transcript, defaultCurrency, preferredLanguage)
}

func (c *Client) transcribe(ctx context.Context, audioBytes []byte, mimeType string) (string, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", "voice"+extensionForMimeType(mimeType))
	if err != nil {
		return "", fmt.Errorf("create multipart file field: %w", err)
	}
	if _, err := part.Write(audioBytes); err != nil {
		return "", fmt.Errorf("write audio bytes: %w", err)
	}
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		return "", fmt.Errorf("write model field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/audio/transcriptions", &buf)
	if err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read transcription response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai transcription API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("unmarshal transcription response: %w", err)
	}
	return result.Text, nil
}

// extensionForMimeType maps a voice note's mime type to a filename
// extension for the multipart upload — Whisper infers the audio format from
// the filename, not the Content-Type header.
func extensionForMimeType(mimeType string) string {
	switch mimeType {
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/m4a", "audio/mp4":
		return ".m4a"
	default:
		return ".ogg" // Telegram voice notes are OGG/Opus by default
	}
}
