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

func (c *Client) ProviderName() string { return "openai" }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	ResponseFormat struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// chat sends a system+user message pair and parses the response, retrying
// once with a repair instruction if the model's output isn't valid JSON or
// fails schema validation — same pattern as the Gemini client.
func (c *Client) chat(ctx context.Context, systemPrompt, userContent string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	reqBody := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
	}
	reqBody.ResponseFormat.Type = "json_object"

	start := time.Now()
	resp, err := c.doChatRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := tryParseChat(resp)
	if !ok {
		reqBody.Messages = append(reqBody.Messages, chatMessage{
			Role:    "user",
			Content: "Your previous response was not valid JSON matching the required schema. Return ONLY the JSON object, no other text.",
		})
		resp, err = c.doChatRequest(ctx, reqBody)
		if err != nil {
			return nil, nil, err
		}
		parsed, ok = tryParseChat(resp)
		if !ok {
			return &ai.ParsedTransaction{
				IsAmbiguous:           true,
				ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
			}, nil, nil
		}
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

func tryParseChat(resp *chatResponse) (*ai.ParsedTransaction, bool) {
	if len(resp.Choices) == 0 {
		return nil, false
	}
	raw := ai.StripJSONFences(resp.Choices[0].Message.Content)
	var pt ai.ParsedTransaction
	if err := json.Unmarshal([]byte(raw), &pt); err != nil {
		return nil, false
	}
	if !ai.ValidateParsedTransaction(&pt) {
		return nil, false
	}
	return &pt, true
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

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.BuildTransactionSystemPrompt(defaultCurrency, time.Now())
	return c.chat(ctx, systemPrompt, "User message: "+userText)
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.BuildTransactionSystemPrompt(defaultCurrency, time.Now())
	userContent := fmt.Sprintf(
		"Original user message: %s\nThe user was asked to clarify and replied: %s\nCombine both messages into a single transaction extraction.",
		originalText, clarificationReply,
	)
	return c.chat(ctx, systemPrompt, userContent)
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	transcript, err := c.transcribe(ctx, audioBytes, mimeType)
	if err != nil {
		return nil, nil, fmt.Errorf("transcribe audio: %w", err)
	}
	return c.ParseTransactionText(ctx, transcript, defaultCurrency)
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
