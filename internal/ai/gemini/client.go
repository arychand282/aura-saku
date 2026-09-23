package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"aura-saku/internal/ai"
	"aura-saku/internal/ai/prompt"
)

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

func New(apiKey, model string) *Client {
	return &Client{
		apiKey:     apiKey,
		model:      model,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

var _ ai.AIClient = (*Client)(nil)

func (c *Client) ProviderName() string { return "gemini" }

type genPart struct {
	Text       string      `json:"text,omitempty"`
	InlineData *inlineData `json:"inline_data,omitempty"`
}

type inlineData struct {
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
}

type genRequest struct {
	Contents []genContent `json:"contents"`
	GenerationConfig struct {
		ResponseMimeType string `json:"responseMimeType"`
	} `json:"generationConfig"`
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

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)\\s*```")

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if m := fenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

func validate(pt *ai.ParsedTransaction) bool {
	switch pt.Type {
	case "expense", "income", "investment", "transfer", "reward":
	default:
		return false
	}
	if !pt.IsAmbiguous && pt.Amount <= 0 {
		return false
	}
	return true
}

// generate sends the given content parts to Gemini and parses the response
// into a ParsedTransaction, retrying once with a repair instruction if the
// model's output isn't valid JSON or fails schema validation.
func (c *Client) generate(ctx context.Context, parts []genPart) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	reqBody := genRequest{
		Contents: []genContent{{Role: "user", Parts: parts}},
	}
	reqBody.GenerationConfig.ResponseMimeType = "application/json"

	start := time.Now()
	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := c.tryParse(resp)
	if !ok {
		// Repair attempt: ask the model again with an explicit correction instruction.
		repairParts := append(append([]genPart{}, parts...), genPart{
			Text: "Your previous response was not valid JSON matching the required schema. Return ONLY the JSON object, no other text.",
		})
		reqBody.Contents = []genContent{{Role: "user", Parts: repairParts}}
		resp, err = c.doRequest(ctx, reqBody)
		if err != nil {
			return nil, nil, err
		}
		parsed, ok = c.tryParse(resp)
		if !ok {
			return &ai.ParsedTransaction{
				IsAmbiguous:           true,
				ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
			}, nil, nil
		}
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.UsageMetadata.PromptTokenCount,
		CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

func (c *Client) tryParse(resp *genResponse) (*ai.ParsedTransaction, bool) {
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, false
	}
	raw := stripFences(resp.Candidates[0].Content.Parts[0].Text)
	var pt ai.ParsedTransaction
	if err := json.Unmarshal([]byte(raw), &pt); err != nil {
		return nil, false
	}
	if !validate(&pt) {
		return nil, false
	}
	return &pt, true
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

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	parts := []genPart{
		{Text: fmt.Sprintf(prompt.TransactionSystemPrompt, defaultCurrency)},
		{Text: "User message: " + userText},
	}
	return c.generate(ctx, parts)
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	parts := []genPart{
		{Text: fmt.Sprintf(prompt.TransactionSystemPrompt, defaultCurrency)},
		{Text: "Transcribe the following voice note and extract the transaction it describes."},
		{InlineData: &inlineData{
			MimeType: mimeType,
			Data:     base64.StdEncoding.EncodeToString(audioBytes),
		}},
	}
	return c.generate(ctx, parts)
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string) (*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	parts := []genPart{
		{Text: fmt.Sprintf(prompt.TransactionSystemPrompt, defaultCurrency)},
		{Text: "Original user message: " + originalText},
		{Text: "The user was asked to clarify and replied: " + clarificationReply},
		{Text: "Combine both messages into a single transaction extraction."},
	}
	return c.generate(ctx, parts)
}
