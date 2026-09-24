package messaging

import "context"

// IncomingMessage is the platform-normalized shape every Messenger
// implementation produces from a raw webhook payload, so bot.Service never
// needs to know whether the source was Telegram or (later) WhatsApp.
type IncomingMessage struct {
	ChatID       int64
	UserID       int64
	Text         string
	IsVoice      bool
	VoiceFileID  string
	Command      string
	CommandArgs  string
	RawUpdateID  int64
	LanguageCode string // raw platform locale (e.g. Telegram's language_code), empty if unavailable
}

// Messenger abstracts the chat platform so Telegram (Phase 1) and WhatsApp
// (future) can both satisfy bot.Service without any core logic change.
type Messenger interface {
	// ParseWebhookPayload converts a raw inbound HTTP body into a normalized IncomingMessage.
	ParseWebhookPayload(body []byte) (*IncomingMessage, error)

	SendMessage(ctx context.Context, chatID int64, text string) error
	SendTypingIndicator(ctx context.Context, chatID int64) error

	// DownloadFile fetches a voice/media file by platform-specific fileID,
	// returning raw bytes + detected mime type for AIClient.TranscribeAndParseAudio.
	DownloadFile(ctx context.Context, fileID string) (data []byte, mimeType string, err error)

	PlatformName() string
}
