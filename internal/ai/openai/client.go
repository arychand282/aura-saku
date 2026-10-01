package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
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
// ToolCalls, no Content), a tool-result turn (Role: "tool" + ToolCallID +
// Content) — see chain.go for the only place that last shape is used — and
// a multimodal user turn (Role + ContentParts, for AnalyzeReceiptImage).
// ContentParts and Content are mutually exclusive; MarshalJSON below picks
// whichever is set so a plain Content field keeps producing a plain JSON
// string exactly as before (this must be exact — chain.go's tool-call-replay
// messages depend on the existing behavior where both being empty/nil omits
// "content" entirely; encoding/json's built-in omitempty on an `any` field
// can't be trusted to do this correctly, since it only checks whether the
// interface itself is nil, not whether a boxed empty string is "empty" — a
// naive `Content any` widening would have started emitting `"content":""`
// for the assistant-replaying-a-tool-call shape).
type chatMessage struct {
	Role         string
	Content      string
	ContentParts []visionContentPart
	ToolCalls    []toolCall
	ToolCallID   string
}

func (m chatMessage) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       string      `json:"role"`
		Content    interface{} `json:"content,omitempty"`
		ToolCalls  []toolCall  `json:"tool_calls,omitempty"`
		ToolCallID string      `json:"tool_call_id,omitempty"`
	}
	w := wire{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID}
	switch {
	case m.ContentParts != nil:
		w.Content = m.ContentParts
	case m.Content != "":
		w.Content = m.Content
	}
	return json.Marshal(w)
}

// visionContentPart is one part of a multimodal user message's content
// array — OpenAI's Chat Completions API expects "content" to be an array of
// these (rather than a plain string) when an image is included.
type visionContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *imageURLPart `json:"image_url,omitempty"`
}

type imageURLPart struct {
	URL string `json:"url"`
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

// historyToMessages converts prior conversation turns into leading chat
// messages, inserted between the system message and the current one.
// ai.ConversationTurn.Role ("user"/"assistant") already matches OpenAI's
// own role vocabulary — no translation needed, unlike Gemini's "model".
func historyToMessages(turns []ai.ConversationTurn) []chatMessage {
	msgs := make([]chatMessage, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, chatMessage{Role: t.Role, Content: t.Content})
	}
	return msgs
}

// chat sends a system+history+user message sequence plus the declared
// transaction tools and derives one ParsedTransaction per tool call the
// model decided to make — almost always one, but record_transaction may be
// called several times in a single turn for a message listing several
// distinct transactions (see internal/ai/prompt/tool_schema.go). No
// repair-retry loop: a tool_call's Arguments are schema-guaranteed by
// OpenAI to match the declared parameters, and a message with no
// tool_calls is the model choosing to reply in plain text instead (this
// provider's "ambiguous" case) — there's no third case of "text that was
// supposed to be JSON but isn't quite."
//
// history prepends prior conversation turns after the system message and
// folds history.Summary into it, so a follow-up message is understood in
// context instead of parsed as if it were the first message ever sent.
// The zero value ConversationContext{} behaves exactly as this method did
// before history existed.
func (c *Client) chat(ctx context.Context, systemPrompt, userContent string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	return c.chatWithMessage(ctx, systemPrompt, chatMessage{Role: "user", Content: userContent}, history)
}

// chatWithMessage is chat's more general form, taking a pre-built final
// user chatMessage instead of a plain string — the one caller that needs
// this is AnalyzeReceiptImage, whose user turn is a multimodal
// (ContentParts) message rather than plain text.
func (c *Client) chatWithMessage(ctx context.Context, systemPrompt string, userMsg chatMessage, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	messages := append([]chatMessage{{Role: "system", Content: systemPrompt + prompt.ConversationSummaryPreamble(history.Summary)}}, historyToMessages(history.Recent)...)
	messages = append(messages, userMsg)

	reqBody := chatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    transactionTools(),
	}

	start := time.Now()
	resp, err := c.doChatRequest(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}

	parsed, ok := tryParseToolCalls(resp)
	if !ok {
		return []*ai.ParsedTransaction{{
			IsAmbiguous:           true,
			ClarificationQuestion: "Sorry, I couldn't quite understand that. Could you tell me the amount and what it was for?",
		}}, nil, nil
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return parsed, usage, nil
}

func tryParseToolCalls(resp *chatResponse) ([]*ai.ParsedTransaction, bool) {
	if len(resp.Choices) == 0 {
		return nil, false
	}
	msg := resp.Choices[0].Message

	if len(msg.ToolCalls) > 0 {
		results := make([]*ai.ParsedTransaction, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, false
			}
			pt := ai.ParsedTransactionFromToolCall(tc.Function.Name, args)
			if pt == nil || !ai.ValidateParsedTransaction(pt) {
				return nil, false
			}
			results = append(results, pt)
		}
		return results, true
	}

	if msg.Content == "" {
		return nil, false
	}
	return []*ai.ParsedTransaction{{IsAmbiguous: true, ClarificationQuestion: msg.Content}}, true
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

func (c *Client) ParseTransactionText(ctx context.Context, userText string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	return c.chat(ctx, systemPrompt, "User message: "+userText, history)
}

func (c *Client) AnswerClarification(ctx context.Context, originalText string, clarificationReply string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	userContent := fmt.Sprintf(
		"Original user message: %s\nThe user was asked to clarify and replied: %s\nCombine both messages into a single transaction extraction.",
		originalText, clarificationReply,
	)
	return c.chat(ctx, systemPrompt, userContent, history)
}

func (c *Client) TranscribeAndParseAudio(ctx context.Context, audioBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	transcript, err := c.transcribe(ctx, audioBytes, mimeType)
	if err != nil {
		return nil, nil, fmt.Errorf("transcribe audio: %w", err)
	}
	return c.ParseTransactionText(ctx, transcript, defaultCurrency, preferredLanguage, history)
}

func (c *Client) AnalyzeReceiptImage(ctx context.Context, imageBytes []byte, mimeType string, defaultCurrency string, preferredLanguage string, caption string, history ai.ConversationContext) ([]*ai.ParsedTransaction, *ai.UsageMetadata, error) {
	systemPrompt := prompt.ToolCallingSystemInstruction(defaultCurrency, time.Now(), preferredLanguage)
	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeType, base64.StdEncoding.EncodeToString(imageBytes))
	userMsg := chatMessage{
		Role: "user",
		ContentParts: []visionContentPart{
			{Type: "text", Text: prompt.ReceiptAnalysisInstruction(caption)},
			{Type: "image_url", ImageURL: &imageURLPart{URL: dataURL}},
		},
	}
	return c.chatWithMessage(ctx, systemPrompt, userMsg, history)
}

// Summarize sends priorSummary plus turns as a plain text-in/text-out
// completion (no tools declared — there's nothing to extract, only
// condense) and returns the model's updated summary text verbatim.
func (c *Client) Summarize(ctx context.Context, priorSummary string, turns []ai.ConversationTurn, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	lead := "Conversation so far (write a summary of it):"
	if priorSummary != "" {
		lead = "Prior summary: " + priorSummary + "\n\nNew messages since that summary (merge them into one updated summary):"
	}
	// lead is the LAST message, not the first — after the system prompt
	// comes the raw history, then the actual instruction to act on it now,
	// matching gemini.Client.Summarize (found live there: some providers
	// reject or handle worse a request that doesn't end on the turn asking
	// for a response).
	messages := append([]chatMessage{{Role: "system", Content: prompt.SummarizationSystemInstruction(preferredLanguage)}}, historyToMessages(turns)...)
	messages = append(messages, chatMessage{Role: "user", Content: lead})

	start := time.Now()
	resp, err := c.doChatRequest(ctx, chatRequest{Model: c.model, Messages: messages})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil, fmt.Errorf("empty summarization response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), usage, nil
}

// SuggestSavings is Summarize's sibling: same plain text-in/text-out shape
// (no tools, no history needed), just a different system instruction and a
// single spendingSummary message instead of a conversation to condense.
func (c *Client) SuggestSavings(ctx context.Context, spendingSummary string, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	messages := []chatMessage{
		{Role: "system", Content: prompt.SavingsSuggestionSystemInstruction(preferredLanguage)},
		{Role: "user", Content: spendingSummary},
	}

	start := time.Now()
	resp, err := c.doChatRequest(ctx, chatRequest{Model: c.model, Messages: messages})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil, fmt.Errorf("empty savings suggestion response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), usage, nil
}

func (c *Client) PhraseTransactionList(ctx context.Context, listSummary string, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	messages := []chatMessage{
		{Role: "system", Content: prompt.SpendingListPhraseSystemInstruction(preferredLanguage)},
		{Role: "user", Content: listSummary},
	}

	start := time.Now()
	resp, err := c.doChatRequest(ctx, chatRequest{Model: c.model, Messages: messages})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil, fmt.Errorf("empty spending list phrasing response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), usage, nil
}

func (c *Client) PhraseSpendingBreakdown(ctx context.Context, breakdownSummary string, preferredLanguage string) (string, *ai.UsageMetadata, error) {
	messages := []chatMessage{
		{Role: "system", Content: prompt.SpendingBreakdownPhraseSystemInstruction(preferredLanguage)},
		{Role: "user", Content: breakdownSummary},
	}

	start := time.Now()
	resp, err := c.doChatRequest(ctx, chatRequest{Model: c.model, Messages: messages})
	if err != nil {
		return "", nil, err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", nil, fmt.Errorf("empty spending breakdown phrasing response")
	}

	usage := &ai.UsageMetadata{
		Model:            c.model,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		LatencyMs:        int(time.Since(start).Milliseconds()),
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), usage, nil
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
