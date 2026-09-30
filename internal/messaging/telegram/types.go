package telegram

// Update and its nested types model the subset of the Telegram Bot API
// webhook payload this bot cares about.
// https://core.telegram.org/bots/api#update
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64       `json:"message_id"`
	From      *User       `json:"from"`
	Chat      Chat        `json:"chat"`
	Text      string      `json:"text"`
	Voice     *Voice      `json:"voice"`
	Photo     []PhotoSize `json:"photo"`
	Caption   string      `json:"caption"`
}

type User struct {
	ID           int64  `json:"id"`
	LanguageCode string `json:"language_code"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
}

// PhotoSize is one resolution of an uploaded photo — Telegram generates
// several per photo, ordered smallest to largest; ParseWebhookPayload picks
// the last (largest) one for the best receipt-reading quality.
type PhotoSize struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
