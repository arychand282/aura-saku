package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestSendMessage_UsesMarkdownParseMode reproduces a real reported bug: the
// AI's own free-text replies use single-asterisk *bold* (Telegram's actual
// syntax), but SendMessage sent no parse_mode at all, so Telegram rendered
// it as literal asterisks instead of bold text. The request must carry
// parse_mode: "Markdown".
func TestSendMessage_UsesMarkdownParseMode(t *testing.T) {
	var gotBody map[string]any
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))

	if err := c.SendMessage(context.Background(), 123, "*Internet:* Rp375.000,00"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if gotBody["parse_mode"] != "Markdown" {
		t.Errorf("parse_mode = %v, want %q", gotBody["parse_mode"], "Markdown")
	}
	if gotBody["text"] != "*Internet:* Rp375.000,00" {
		t.Errorf("text = %v, want the original message unchanged", gotBody["text"])
	}
}

// TestSendMessage_FallsBackToPlainTextWhenMarkdownRejected covers an
// AI-generated reply with unbalanced markdown entities (e.g. a stray
// underscore in an echoed-back user description) — Telegram's Bot API
// rejects the whole send outright in that case ("can't parse entities").
// The message must still be delivered, degrading to a second plain-text
// attempt rather than silently never reaching the user at all.
func TestSendMessage_FallsBackToPlainTextWhenMarkdownRejected(t *testing.T) {
	var calls []map[string]any
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		if body["parse_mode"] == "Markdown" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ok":false,"description":"Bad Request: can't parse entities"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))

	if err := c.SendMessage(context.Background(), 123, "beli barang_bagus 50rb"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d requests, want 2 (markdown attempt, then plain-text fallback)", len(calls))
	}
	if calls[0]["parse_mode"] != "Markdown" {
		t.Errorf("first call parse_mode = %v, want %q", calls[0]["parse_mode"], "Markdown")
	}
	if _, ok := calls[1]["parse_mode"]; ok {
		t.Errorf("fallback call parse_mode = %v, want the field entirely absent (plain text)", calls[1]["parse_mode"])
	}
	if calls[1]["text"] != "beli barang_bagus 50rb" {
		t.Errorf("fallback text = %v, want the original message unchanged", calls[1]["text"])
	}
}
