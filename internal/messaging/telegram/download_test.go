package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// redirectTransport rewrites every outbound request to target's scheme/host,
// letting a test point Client's hardcoded apiRoot-based URLs at a local
// httptest.Server without needing apiRoot itself to be configurable.
type redirectTransport struct {
	target *url.URL
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.target.Scheme
	req.URL.Host = t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return &Client{
		botToken:   "test-token",
		httpClient: &http.Client{Transport: &redirectTransport{target: target}},
	}
}

// TestDownloadFile_FallbackMimeTypeWinsOverGenericContentType reproduces a
// real reported bug: a receipt photo was consistently misread by a
// vision-capable AI call, and debugging traced it to Telegram's
// file-serving endpoint returning a generic "application/octet-stream"
// Content-Type for the download — not an ABSENT header, which is all the
// old code checked for. A present-but-useless Content-Type must not win
// over what the caller already knows is correct for what it requested.
func TestDownloadFile_FallbackMimeTypeWinsOverGenericContentType(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bottest-token/getFile":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"result":{"file_path":"photos/file_1.jpg"}}`)
		case "/file/bottest-token/photos/file_1.jpg":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("fake jpeg bytes"))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	data, mimeType, err := c.DownloadFile(context.Background(), "file123", "image/jpeg")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if mimeType != "image/jpeg" {
		t.Errorf("mimeType = %q, want %q (the caller-supplied type must win over Telegram's generic application/octet-stream)", mimeType, "image/jpeg")
	}
	if string(data) != "fake jpeg bytes" {
		t.Errorf("data = %q, want the downloaded bytes unchanged", data)
	}
}

// TestDownloadFile_UsesContentTypeWhenCallerGivesNone covers the other
// direction: a caller that genuinely doesn't know the type in advance
// (fallbackMimeType == "") should still get whatever real Content-Type
// Telegram's response provides, rather than always ending up empty.
func TestDownloadFile_UsesContentTypeWhenCallerGivesNone(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bottest-token/getFile":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true,"result":{"file_path":"documents/file_2.pdf"}}`)
		case "/file/bottest-token/documents/file_2.pdf":
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("fake pdf bytes"))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	_, mimeType, err := c.DownloadFile(context.Background(), "file456", "")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if mimeType != "application/pdf" {
		t.Errorf("mimeType = %q, want %q (Telegram's real Content-Type, since the caller asserted no known type)", mimeType, "application/pdf")
	}
}
