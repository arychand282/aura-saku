package fx

import (
	"context"
	"testing"
	"time"
)

// TestLiveService_GetExchangeRate is a real network smoke test against
// Frankfurter. Skipped (not failed) if the network/provider is unreachable,
// so `go test ./...` still passes offline.
func TestLiveService_GetExchangeRate(t *testing.T) {
	svc := NewLiveService("", NewStaticService())
	ctx := context.Background()

	rate, err := svc.GetExchangeRate(ctx, "USD", "IDR", time.Now().UTC())
	if err != nil {
		t.Skipf("skipping, live FX provider unreachable: %v", err)
	}
	if rate <= 1000 {
		t.Fatalf("expected a plausible USD->IDR rate (thousands), got %v", rate)
	}

	historical, err := svc.GetExchangeRate(ctx, "USD", "IDR", time.Date(2024, 6, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Skipf("skipping historical check, live FX provider unreachable: %v", err)
	}
	if historical <= 1000 {
		t.Fatalf("expected a plausible historical USD->IDR rate, got %v", historical)
	}

	same, err := svc.GetExchangeRate(ctx, "IDR", "IDR", time.Now().UTC())
	if err != nil {
		t.Fatalf("same-currency GetExchangeRate should never error: %v", err)
	}
	if same != 1 {
		t.Fatalf("expected same-currency rate of 1, got %v", same)
	}
}

// TestLiveService_FallsBackOnProviderFailure deliberately points at an
// unreachable host (not the real network) so the fallback path is tested
// deterministically, without depending on Frankfurter's actual availability.
func TestLiveService_FallsBackOnProviderFailure(t *testing.T) {
	svc := NewLiveService("http://127.0.0.1:1", NewStaticService()) // nothing listens here

	ctx := context.Background()
	got, err := svc.GetExchangeRate(ctx, "USD", "IDR", time.Now().UTC())
	if err != nil {
		t.Fatalf("expected a fallback to the static table, not an error: %v", err)
	}

	want, _ := NewStaticService().GetExchangeRate(ctx, "USD", "IDR", time.Now().UTC())
	if got != want {
		t.Fatalf("expected fallback rate %v (from static table), got %v", want, got)
	}
}
