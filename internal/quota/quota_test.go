package quota

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

func TestIncrement(t *testing.T) {
	client := newTestRedis(t)
	chatID := time.Now().UnixNano() // unique per run, avoids clashing with real data
	ctx := context.Background()
	key := Key(chatID)
	t.Cleanup(func() { client.Del(ctx, key) })

	for i := 1; i <= 3; i++ {
		if err := Increment(ctx, client, chatID); err != nil {
			t.Fatalf("Increment #%d: %v", i, err)
		}
		got, err := client.Get(ctx, key).Int()
		if err != nil {
			t.Fatalf("get after increment #%d: %v", i, err)
		}
		if got != i {
			t.Fatalf("after %d increments, expected counter %d, got %d", i, i, got)
		}
	}

	ttl, err := client.TTL(ctx, key).Result()
	if err != nil {
		t.Fatalf("get TTL: %v", err)
	}
	if ttl <= 0 || ttl > 26*time.Hour {
		t.Fatalf("expected TTL between 0 and 26h, got %v", ttl)
	}
}

func TestKey_MatchesAcrossCalls(t *testing.T) {
	// Key must be deterministic for the same chat within the same day, since
	// the middleware and bot.Service each compute it independently.
	a := Key(12345)
	b := Key(12345)
	if a != b {
		t.Fatalf("expected same key for repeated calls, got %q vs %q", a, b)
	}
}
