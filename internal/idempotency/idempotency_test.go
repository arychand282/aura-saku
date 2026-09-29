package idempotency

import (
	"context"
	"testing"

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

func TestMarkSeen_FirstThenDuplicate(t *testing.T) {
	client := newTestRedis(t)
	ctx := context.Background()

	first, err := MarkSeen(ctx, client, "test-namespace", "12345", WebhookRedeliveryTTL)
	if err != nil {
		t.Fatalf("MarkSeen (first): %v", err)
	}
	if !first {
		t.Fatal("expected first MarkSeen call to return true (new)")
	}
	t.Cleanup(func() { client.Del(ctx, "aura:seen:test-namespace:12345") })

	second, err := MarkSeen(ctx, client, "test-namespace", "12345", WebhookRedeliveryTTL)
	if err != nil {
		t.Fatalf("MarkSeen (duplicate): %v", err)
	}
	if second {
		t.Fatal("expected duplicate MarkSeen call to return false")
	}

	// A different ID in the same namespace must be independent.
	other, err := MarkSeen(ctx, client, "test-namespace", "different-id", WebhookRedeliveryTTL)
	if err != nil {
		t.Fatalf("MarkSeen (different id): %v", err)
	}
	if !other {
		t.Fatal("expected a different id to be treated as new")
	}
	client.Del(ctx, "aura:seen:test-namespace:different-id")

	// Same id in a different namespace must also be independent (no
	// cross-namespace key collisions).
	otherNamespace, err := MarkSeen(ctx, client, "other-namespace", "12345", WebhookRedeliveryTTL)
	if err != nil {
		t.Fatalf("MarkSeen (other namespace): %v", err)
	}
	if !otherNamespace {
		t.Fatal("expected same id under a different namespace to be treated as new")
	}
	client.Del(ctx, "aura:seen:other-namespace:12345")
}
