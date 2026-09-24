package auth

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"aura-saku/internal/apperror"
)

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("skipping, redis not reachable: %v", err)
	}
	return client
}

const testSecret = "test-jwt-secret-do-not-use-in-prod"

func TestLoginToken_SingleUse(t *testing.T) {
	client := newTestRedis(t)
	ctx := context.Background()

	token, err := IssueLoginToken(ctx, client, 42)
	if err != nil {
		t.Fatalf("IssueLoginToken: %v", err)
	}

	userID, err := ConsumeLoginToken(ctx, client, token)
	if err != nil {
		t.Fatalf("ConsumeLoginToken: %v", err)
	}
	if userID != 42 {
		t.Fatalf("expected userID 42, got %d", userID)
	}

	// Second consume of the same token must fail — single use.
	if _, err := ConsumeLoginToken(ctx, client, token); err != apperror.ErrInvalidLoginToken {
		t.Fatalf("expected ErrInvalidLoginToken on reuse, got %v", err)
	}
}

func TestConsumeLoginToken_Unknown(t *testing.T) {
	client := newTestRedis(t)
	ctx := context.Background()

	if _, err := ConsumeLoginToken(ctx, client, "never-issued"); err != apperror.ErrInvalidLoginToken {
		t.Fatalf("expected ErrInvalidLoginToken, got %v", err)
	}
}

func TestSession_CreateVerifyRevoke(t *testing.T) {
	client := newTestRedis(t)
	ctx := context.Background()

	token, err := CreateSession(ctx, client, testSecret, time.Hour, RoleUser, 7, 0)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	claims, err := VerifySession(ctx, client, testSecret, token)
	if err != nil {
		t.Fatalf("VerifySession: %v", err)
	}
	if claims.Role != RoleUser || claims.UserID != 7 {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	if err := RevokeSession(ctx, client, claims.SessionID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	// A revoked session's JWT still verifies its signature, but must now be
	// rejected because its session ID is gone from Redis.
	if _, err := VerifySession(ctx, client, testSecret, token); err != apperror.ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession after revoke, got %v", err)
	}
}

func TestVerifySession_WrongSecret(t *testing.T) {
	client := newTestRedis(t)
	ctx := context.Background()

	token, err := CreateSession(ctx, client, testSecret, time.Hour, RoleAdmin, 0, 3)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if _, err := VerifySession(ctx, client, "a-different-secret", token); err != apperror.ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession for wrong secret, got %v", err)
	}
}

func TestVerifySession_Garbage(t *testing.T) {
	client := newTestRedis(t)
	ctx := context.Background()

	if _, err := VerifySession(ctx, client, testSecret, "not-a-jwt-at-all"); err != apperror.ErrInvalidSession {
		t.Fatalf("expected ErrInvalidSession for garbage token, got %v", err)
	}
}
