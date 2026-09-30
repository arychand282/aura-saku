package messaging

import "context"

// IncomingMessage is the platform-normalized shape every Messenger
// implementation produces from a raw webhook payload, so bot.Service never
// needs to know whether the source was Telegram or (later) WhatsApp.
type IncomingMessage struct {
	ChatID      int64
	UserID      int64
	Text        string
	IsVoice     bool
	VoiceFileID string
	IsPhoto     bool
	PhotoFileID string
	// Caption is the optional text attached alongside a photo (or other
	// media) — not photo-specific by name since Telegram's own caption
	// field applies to any media message type, even though only the photo
	// path reads it today.
	Caption      string
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
	// returning raw bytes + detected mime type for
	// AIClient.TranscribeAndParseAudio/AnalyzeReceiptImage. fallbackMimeType
	// should be the type the caller already knows is correct for what it
	// requested (e.g. "audio/ogg" for a voice note, "image/jpeg" for a
	// photo) — implementations trust this over whatever the platform's own
	// download response reports, falling back to that response's
	// Content-Type only when the caller passes "". This isn't just a safety
	// net for a missing header: Telegram's file-serving endpoint has been
	// confirmed (empirically, debugging a real receipt-photo misread) to
	// report a generic "application/octet-stream" for at least some photo
	// downloads — a present but useless Content-Type, not an absent one —
	// which would otherwise reach a vision-capable AI call mislabeled as
	// opaque binary data regardless of how legible the actual photo is.
	DownloadFile(ctx context.Context, fileID string, fallbackMimeType string) (data []byte, mimeType string, err error)

	PlatformName() string
}
