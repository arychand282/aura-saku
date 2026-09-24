// Package auth implements two related but distinct Redis-backed flows:
//
//  1. Login tokens — short-lived, single-use tokens the bot issues via
//     /dashboard, exchanged once for a session.
//  2. Sessions — longer-lived JWTs referenced by an httpOnly cookie. The JWT
//     carries role/identity claims so a frontend can read them client-side
//     (e.g. to decide which menu to render) without an extra API round
//     trip, but each token also carries a random session ID that must still
//     exist in Redis — logout or account deactivation deletes that entry,
//     so revocation is instant even though the JWT signature itself would
//     otherwise still verify until natural expiry.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"

	"aura-saku/internal/apperror"
)

// SessionCookieName is the httpOnly cookie both the auth handlers and the
// RequireSession middleware read/write.
const SessionCookieName = "aura_session"

// Role identifies which of the three permission tiers a session belongs to.
type Role string

const (
	RoleUser       Role = "user"
	RoleAdmin      Role = "admin"
	RoleSuperadmin Role = "superadmin"
)

// Claims is the JWT payload. UserID is meaningful when Role == RoleUser;
// AdminID is meaningful when Role is RoleAdmin or RoleSuperadmin.
type Claims struct {
	jwt.RegisteredClaims
	Role      Role   `json:"role"`
	UserID    uint   `json:"user_id,omitempty"`
	AdminID   uint   `json:"admin_id,omitempty"`
	SessionID string `json:"sid"`
}

const loginTokenTTL = 10 * time.Minute

func loginTokenKey(token string) string { return fmt.Sprintf("aura:logintoken:%s", token) }
func sessionKey(sid string) string      { return fmt.Sprintf("aura:session:%s", sid) }

func randomToken(numBytes int) (string, error) {
	b := make([]byte, numBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IssueLoginToken generates a single-use, short-lived token for a Telegram
// user, to be embedded in the /dashboard login link.
func IssueLoginToken(ctx context.Context, redisClient *redis.Client, userID uint) (string, error) {
	token, err := randomToken(24)
	if err != nil {
		return "", fmt.Errorf("generate login token: %w", err)
	}
	if err := redisClient.Set(ctx, loginTokenKey(token), strconv.FormatUint(uint64(userID), 10), loginTokenTTL).Err(); err != nil {
		return "", fmt.Errorf("store login token: %w", err)
	}
	return token, nil
}

// ConsumeLoginToken validates and immediately deletes a login token
// (single-use), returning the User ID it was issued for.
func ConsumeLoginToken(ctx context.Context, redisClient *redis.Client, token string) (uint, error) {
	key := loginTokenKey(token)
	val, err := redisClient.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return 0, apperror.ErrInvalidLoginToken
		}
		return 0, err
	}
	redisClient.Del(ctx, key)

	id, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse login token payload: %w", err)
	}
	return uint(id), nil
}

// CreateSession issues a signed JWT for the given identity and records its
// session ID in Redis (with the same TTL as the token) so it can be
// revoked before natural expiry.
func CreateSession(ctx context.Context, redisClient *redis.Client, jwtSecret string, ttl time.Duration, role Role, userID, adminID uint) (string, error) {
	sid, err := randomToken(16)
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}

	now := time.Now().UTC()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
		Role:      role,
		UserID:    userID,
		AdminID:   adminID,
		SessionID: sid,
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret))
	if err != nil {
		return "", fmt.Errorf("sign session token: %w", err)
	}

	if err := redisClient.Set(ctx, sessionKey(sid), "active", ttl).Err(); err != nil {
		return "", fmt.Errorf("store session: %w", err)
	}

	return signed, nil
}

// VerifySession checks the JWT signature/expiry and that its session ID is
// still present in Redis (i.e. hasn't been logged out or otherwise revoked).
func VerifySession(ctx context.Context, redisClient *redis.Client, jwtSecret string, tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return nil, apperror.ErrInvalidSession
	}

	exists, err := redisClient.Exists(ctx, sessionKey(claims.SessionID)).Result()
	if err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, apperror.ErrInvalidSession
	}

	return claims, nil
}

// RevokeSession deletes a session's Redis entry, making its JWT worthless
// immediately regardless of its remaining signature validity.
func RevokeSession(ctx context.Context, redisClient *redis.Client, sessionID string) error {
	return redisClient.Del(ctx, sessionKey(sessionID)).Err()
}
