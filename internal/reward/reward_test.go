package reward

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("skipping, redis not reachable: %v", err)
	}
	return client
}

func TestClaim_FullFlow(t *testing.T) {
	client := newTestRedis(t)
	chatID := time.Now().UnixNano()
	ctx := context.Background()
	t.Cleanup(func() {
		client.Del(ctx, codeKey(chatID), claimsKey(chatID), bonusKey(chatID))
	})

	code, err := IssueCode(ctx, client, chatID)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	if len(code) != codeLength {
		t.Fatalf("expected code length %d, got %d (%q)", codeLength, len(code), code)
	}

	// Wrong code should be rejected without consuming the real one.
	if result, err := Claim(ctx, client, chatID, "WRONGCODE", 3, 1); err != nil || result != ClaimInvalidCode {
		t.Fatalf("expected ClaimInvalidCode for wrong code, got %v, %v", result, err)
	}

	// Correct code (case-insensitive, with stray whitespace) should succeed.
	result, err := Claim(ctx, client, chatID, " "+code+" ", 3, 1)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if result != ClaimGranted {
		t.Fatalf("expected ClaimGranted, got %v", result)
	}

	bonus, err := BonusGrantedToday(ctx, client, chatID)
	if err != nil {
		t.Fatalf("BonusGrantedToday: %v", err)
	}
	if bonus != 3 {
		t.Fatalf("expected bonus 3, got %d", bonus)
	}

	// Code is consumed — re-using it (even with a fresh Claim call) must fail.
	if result, err := Claim(ctx, client, chatID, code, 3, 1); err != nil || result != ClaimInvalidCode {
		t.Fatalf("expected ClaimInvalidCode for reused code, got %v, %v", result, err)
	}

	// Issue a second code and try to claim it — should hit the daily cap (maxClaimsPerDay=1).
	code2, err := IssueCode(ctx, client, chatID)
	if err != nil {
		t.Fatalf("IssueCode (2nd): %v", err)
	}
	if result, err := Claim(ctx, client, chatID, code2, 3, 1); err != nil || result != ClaimLimitReached {
		t.Fatalf("expected ClaimLimitReached, got %v, %v", result, err)
	}
}

func TestClaim_NoCodeIssued(t *testing.T) {
	client := newTestRedis(t)
	chatID := time.Now().UnixNano()
	ctx := context.Background()

	result, err := Claim(ctx, client, chatID, "ANYCODE", 3, 1)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if result != ClaimInvalidCode {
		t.Fatalf("expected ClaimInvalidCode when no code was ever issued, got %v", result)
	}
}
