package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"aura-saku/internal/apperror"
	"aura-saku/internal/messaging"
)

const apiRoot = "https://api.telegram.org"

// Client implements messaging.Messenger against the Telegram Bot API.
type Client struct {
	botToken   string
	httpClient *http.Client
}

func New(botToken string) *Client {
	return &Client{
		botToken:   botToken,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

var _ messaging.Messenger = (*Client)(nil)

func (c *Client) PlatformName() string { return "telegram" }

func (c *Client) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", apiRoot, c.botToken, method)
}

// ParseWebhookPayload normalizes a Telegram Update into an IncomingMessage.
// A genuinely malformed body (not valid JSON at all) returns a real error.
// A validly-parsed Update with no top-level "message" — an edited message,
// a callback query, a chat-member change, etc. — returns
// apperror.ErrUnhandledUpdateType rather than a generic error: it's an
// expected event type this bot doesn't act on, not a failure, and the
// caller (NewWebhookHandler) acks it 200 rather than 400 so Telegram
// doesn't retry it forever with exponential backoff.
func (c *Client) ParseWebhookPayload(body []byte) (*messaging.IncomingMessage, error) {
	var upd Update
	if err := json.Unmarshal(body, &upd); err != nil {
		return nil, fmt.Errorf("unmarshal telegram update: %w", err)
	}
	if upd.Message == nil {
		return nil, apperror.ErrUnhandledUpdateType
	}

	msg := upd.Message
	incoming := &messaging.IncomingMessage{
		RawUpdateID: upd.UpdateID,
		ChatID:      msg.Chat.ID,
	}
	if msg.From != nil {
		incoming.UserID = msg.From.ID
		incoming.LanguageCode = msg.From.LanguageCode
	}

	if msg.Voice != nil {
		incoming.IsVoice = true
		incoming.VoiceFileID = msg.Voice.FileID
		return incoming, nil
	}

	if len(msg.Photo) > 0 {
		incoming.IsPhoto = true
		incoming.PhotoFileID = msg.Photo[len(msg.Photo)-1].FileID // largest resolution
		incoming.Caption = msg.Caption
		return incoming, nil
	}

	incoming.Text = msg.Text
	if strings.HasPrefix(msg.Text, "/") {
		fields := strings.SplitN(msg.Text, " ", 2)
		incoming.Command = fields[0]
		if len(fields) > 1 {
			incoming.CommandArgs = strings.TrimSpace(fields[1])
		}
	}

	return incoming, nil
}

// SendMessage sends text with Telegram's legacy "Markdown" parse mode so
// single-asterisk *bold* (the only emphasis this bot's system instruction
// asks the AI to use — see prompt.ToolCallingSystemInstruction) actually
// renders instead of showing literally. Legacy mode (not MarkdownV2) is
// used deliberately: MarkdownV2 requires escaping periods/parentheses/
// hyphens everywhere, which this bot's money formatting and templates use
// constantly, while legacy mode only treats _, *, `, and [ as special —
// the AI could still occasionally produce text with an unmatched one of
// those (e.g. a user's own description echoed back containing a stray
// underscore), which Telegram's Bot API rejects outright ("can't parse
// entities") rather than rendering best-effort. Falling back to a second,
// plain-text send on any error keeps that single malformed message from
// going completely undelivered — silently degrading to literal asterisks
// (the original bug) rather than silently dropping the reply entirely.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	payload, err := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"text":       text,
		"parse_mode": "Markdown",
	})
	if err != nil {
		return fmt.Errorf("marshal sendMessage payload: %w", err)
	}
	if err := c.post(ctx, "sendMessage", payload); err != nil {
		plainPayload, plainErr := json.Marshal(map[string]any{
			"chat_id": chatID,
			"text":    text,
		})
		if plainErr != nil {
			return fmt.Errorf("marshal plain-text sendMessage fallback payload: %w", plainErr)
		}
		return c.post(ctx, "sendMessage", plainPayload)
	}
	return nil
}

func (c *Client) SendTypingIndicator(ctx context.Context, chatID int64) error {
	payload, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	})
	if err != nil {
		return fmt.Errorf("marshal sendChatAction payload: %w", err)
	}
	return c.post(ctx, "sendChatAction", payload)
}

func (c *Client) post(ctx context.Context, method string, payload []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL(method), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", method, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram %s error (status %d): %s", method, resp.StatusCode, string(body))
	}
	return nil
}

type getFileResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		FilePath string `json:"file_path"`
	} `json:"result"`
}

func (c *Client) DownloadFile(ctx context.Context, fileID string, fallbackMimeType string) ([]byte, string, error) {
	getFileURL := fmt.Sprintf("%s?file_id=%s", c.apiURL("getFile"), fileID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getFileURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build getFile request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("getFile request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read getFile response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("telegram getFile error (status %d): %s", resp.StatusCode, string(body))
	}

	var gf getFileResponse
	if err := json.Unmarshal(body, &gf); err != nil {
		return nil, "", fmt.Errorf("unmarshal getFile response: %w", err)
	}
	if !gf.OK || gf.Result.FilePath == "" {
		return nil, "", fmt.Errorf("telegram getFile returned no file_path")
	}

	downloadURL := fmt.Sprintf("%s/file/bot%s/%s", apiRoot, c.botToken, gf.Result.FilePath)
	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build file download request: %w", err)
	}

	dlResp, err := c.httpClient.Do(dlReq)
	if err != nil {
		return nil, "", fmt.Errorf("file download failed: %w", err)
	}
	defer dlResp.Body.Close()

	data, err := io.ReadAll(dlResp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("read downloaded file: %w", err)
	}

	// fallbackMimeType takes priority over Telegram's own Content-Type
	// header, not the other way around — confirmed empirically that
	// Telegram's file-serving endpoint often reports a generic
	// "application/octet-stream" for a photo download (not merely an empty
	// header), which would otherwise get passed to a vision-capable AI call
	// as an unrecognized binary blob instead of an image, regardless of how
	// legible the actual photo is. The caller already knows the real type
	// for every message kind this bot handles (photos are always
	// JPEG-recompressed by Telegram server-side, voice notes always
	// OGG/Opus), so that known-correct value is trusted over whatever this
	// endpoint happens to report. Telegram's own header is used only when
	// the caller genuinely doesn't know the type in advance.
	mimeType := fallbackMimeType
	if mimeType == "" {
		mimeType = dlResp.Header.Get("Content-Type")
	}
	return data, mimeType, nil
}
