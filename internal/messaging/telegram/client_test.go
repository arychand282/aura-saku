package telegram_test

import (
	"errors"
	"testing"

	"aura-saku/internal/apperror"
	"aura-saku/internal/messaging/telegram"
)

func TestParseWebhookPayload_ValidMessage(t *testing.T) {
	c := telegram.New("test-token")
	body := []byte(`{
		"update_id": 42,
		"message": {
			"message_id": 1,
			"from": {"id": 100, "language_code": "id"},
			"chat": {"id": 100},
			"text": "lunch 25000"
		}
	}`)

	msg, err := c.ParseWebhookPayload(body)
	if err != nil {
		t.Fatalf("ParseWebhookPayload: %v", err)
	}
	if msg.ChatID != 100 || msg.Text != "lunch 25000" {
		t.Errorf("msg = %+v, want ChatID=100 Text=%q", msg, "lunch 25000")
	}
}

// TestParseWebhookPayload_UnhandledUpdateType covers the bug this test
// guards against: Telegram sends update types this bot doesn't act on
// (edited messages, callback queries, chat-member changes, ...) alongside
// new messages. Before this fix, any of these returned a generic error,
// which the webhook handler turned into a 400 — Telegram then retried the
// same update forever with exponential backoff, since it never received
// the 2xx it needed to stop. This must return the specific
// apperror.ErrUnhandledUpdateType sentinel, not just "an error", so the
// webhook handler can tell "expected, ack it" apart from "genuinely
// malformed, worth logging."
func TestParseWebhookPayload_UnhandledUpdateType(t *testing.T) {
	c := telegram.New("test-token")

	cases := map[string]string{
		"edited_message": `{
			"update_id": 43,
			"edited_message": {"message_id": 1, "chat": {"id": 100}, "text": "lunch 25000 (edited)"}
		}`,
		"callback_query": `{
			"update_id": 44,
			"callback_query": {"id": "abc", "from": {"id": 100}}
		}`,
		"no recognized fields at all": `{"update_id": 45}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := c.ParseWebhookPayload([]byte(body))
			if !errors.Is(err, apperror.ErrUnhandledUpdateType) {
				t.Errorf("err = %v, want apperror.ErrUnhandledUpdateType", err)
			}
		})
	}
}

// TestParseWebhookPayload_MalformedJSON covers the other branch: genuinely
// invalid JSON must NOT be mistaken for apperror.ErrUnhandledUpdateType —
// that would silently ack (200) a payload that isn't a real Telegram update
// at all, e.g. some other client hitting this endpoint by mistake.
func TestParseWebhookPayload_MalformedJSON(t *testing.T) {
	c := telegram.New("test-token")

	_, err := c.ParseWebhookPayload([]byte(`{not valid json`))
	if err == nil {
		t.Fatal("expected an error for malformed JSON, got nil")
	}
	if errors.Is(err, apperror.ErrUnhandledUpdateType) {
		t.Error("malformed JSON must not be classified as apperror.ErrUnhandledUpdateType")
	}
}
