package telegram

// Update and its nested types model the subset of the Telegram Bot API
// webhook payload this bot cares about.
// https://core.telegram.org/bots/api#update
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
	Voice     *Voice `json:"voice"`
}

type User struct {
	ID int64 `json:"id"`
}

type Chat struct {
	ID int64 `json:"id"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
}
