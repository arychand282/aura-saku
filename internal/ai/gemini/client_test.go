package gemini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDoRequest_FallsBackToSecondaryModelOnRetriableError covers the core
// resilience fix: a model that keeps returning 503 ("high demand") gets
// retried (maxAttemptsPerModel times) before doRequest moves on to the
// configured fallback model, which then succeeds.
func TestDoRequest_FallsBackToSecondaryModelOnRetriableError(t *testing.T) {
	var primaryCalls, fallbackCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/primary-model:"):
			atomic.AddInt32(&primaryCalls, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"message":"high demand"}}`))
		case strings.Contains(r.URL.Path, "/fallback-model:"):
			atomic.AddInt32(&fallbackCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := New("test-key", "primary-model", server.URL, "fallback-model")
	resp, err := c.doRequest(context.Background(), genRequest{})
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil response from the fallback model")
	}
	if got := atomic.LoadInt32(&primaryCalls); got != int32(maxAttemptsPerModel) {
		t.Errorf("primaryCalls = %d, want %d (exhausted its own retries before falling back)", got, maxAttemptsPerModel)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 1 {
		t.Errorf("fallbackCalls = %d, want 1 (succeeded on the fallback model's first attempt)", got)
	}
}

// TestDoRequest_NonRetriableErrorFailsImmediately covers that a
// non-retriable status (400, 401, ...) never triggers a retry or a
// fallback-model attempt — retrying a client-side/config problem on a
// different model would never fix it.
func TestDoRequest_NonRetriableErrorFailsImmediately(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"bad request"}}`))
	}))
	defer server.Close()

	c := New("test-key", "primary-model", server.URL, "fallback-model")
	if _, err := c.doRequest(context.Background(), genRequest{}); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a non-retriable error must not retry or fall back)", got)
	}
}

// TestDoRequest_FallbackSameAsPrimaryIsNoOp covers that a configured
// fallback identical to the primary model is never tried as a second
// "model" — there would be nothing to gain from retrying the exact same
// model under its own name twice as many times.
func TestDoRequest_FallbackSameAsPrimaryIsNoOp(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503}}`))
	}))
	defer server.Close()

	c := New("test-key", "same-model", server.URL, "same-model")
	if _, err := c.doRequest(context.Background(), genRequest{}); err == nil {
		t.Fatal("expected an error (server always returns 503)")
	}
	if got := atomic.LoadInt32(&calls); got != int32(maxAttemptsPerModel) {
		t.Errorf("calls = %d, want %d (fallback==model must not double the attempt budget)", got, maxAttemptsPerModel)
	}
}

// TestDoRequest_SucceedsOnFirstAttemptNoRetryNoFallback is the baseline
// regression check: a healthy primary model is called exactly once, with
// no retry and no fallback-model request at all.
func TestDoRequest_SucceedsOnFirstAttemptNoRetryNoFallback(t *testing.T) {
	var primaryCalls, fallbackCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/primary-model:"):
			atomic.AddInt32(&primaryCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/fallback-model:"):
			atomic.AddInt32(&fallbackCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	c := New("test-key", "primary-model", server.URL, "fallback-model")
	if _, err := c.doRequest(context.Background(), genRequest{}); err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	if got := atomic.LoadInt32(&primaryCalls); got != 1 {
		t.Errorf("primaryCalls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&fallbackCalls); got != 0 {
		t.Errorf("fallbackCalls = %d, want 0 (fallback must never be tried when the primary succeeds)", got)
	}
}
