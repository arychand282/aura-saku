package openai

import (
	"encoding/json"
	"testing"
)

// TestChatMessage_MarshalJSON guards the one genuinely risky part of adding
// vision support to this provider: chatMessage.Content stayed a plain
// string (not widened to `any`) specifically because chain.go's
// tool-call-replay message (Role: "assistant", ToolCalls set, Content left
// at its zero value) depends on "content" being omitted entirely from the
// JSON, not emitted as `"content":""` — which is exactly what a naive
// `Content any` + built-in omitempty would have done (omitempty on an
// interface field only checks IsNil, not whether the boxed value is
// "empty"). ContentParts is the new field AnalyzeReceiptImage actually
// uses.
func TestChatMessage_MarshalJSON(t *testing.T) {
	cases := []struct {
		name string
		msg  chatMessage
		want string
	}{
		{
			name: "plain string content — unchanged existing shape",
			msg:  chatMessage{Role: "user", Content: "hello"},
			want: `{"role":"user","content":"hello"}`,
		},
		{
			name: "assistant replaying a tool call — content must be omitted, not \"\"",
			msg:  chatMessage{Role: "assistant", ToolCalls: []toolCall{{ID: "call_1", Type: "function"}}},
			want: `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"","arguments":""}}]}`,
		},
		{
			name: "tool result turn",
			msg:  chatMessage{Role: "tool", ToolCallID: "call_1", Content: `{"ok":true}`},
			want: `{"role":"tool","content":"{\"ok\":true}","tool_call_id":"call_1"}`,
		},
		{
			name: "multimodal content parts — the new receipt-image shape",
			msg: chatMessage{Role: "user", ContentParts: []visionContentPart{
				{Type: "text", Text: "describe this"},
				{Type: "image_url", ImageURL: &imageURLPart{URL: "data:image/jpeg;base64,abc"}},
			}},
			want: `{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,abc"}}]}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := json.Marshal(c.msg)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != c.want {
				t.Errorf("Marshal(%+v) = %s, want %s", c.msg, got, c.want)
			}
		})
	}
}
