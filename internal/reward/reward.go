// Package reward implements the free-tier "earn bonus quota" flow: when a
// user hits their daily AI-prompt limit, the bot can offer an affiliate
// link plus a short one-time code; sending /claim <code> back grants bonus
// quota for the rest of the day.
//
// This is an honor-system design, not a verified one — there is no landing
// page or ad-network callback confirming the user actually visited the
// link. That's a deliberate, scoped-down v1: building real verification
// would require a web page and an ad network/affiliate integration that
// doesn't exist yet. The code requirement exists to add minor friction
// (forces reading the message) and to correlate one offer with one claim,
// not to prevent someone from lying about visiting the link.
package reward

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	codeTTL = 10 * time.Minute
	dayTTL  = 26 * time.Hour // see internal/quota for why 26h, not 24h

	// codeCharset avoids visually ambiguous characters (0/O, 1/I) since the
	// code is read and retyped by a human.
	codeCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	codeLength  = 6
)

func codeKey(chatID int64) string   { return fmt.Sprintf("aura:rewardcode:%d", chatID) }
func claimsKey(chatID int64) string { return fmt.Sprintf("aura:rewardclaims:%d:%s", chatID, today()) }
func bonusKey(chatID int64) string  { return fmt.Sprintf("aura:rewardbonus:%d:%s", chatID, today()) }
func today() string                 { return time.Now().UTC().Format("2006-01-02") }

// ClaimsToday returns how many times a chat has successfully claimed a
// reward today.
func ClaimsToday(ctx context.Context, redisClient *redis.Client, chatID int64) (int, error) {
	n, err := redisClient.Get(ctx, claimsKey(chatID)).Int()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

// BonusGrantedToday returns how much bonus quota a chat has earned today,
// on top of the base free-tier daily limit.
func BonusGrantedToday(ctx context.Context, redisClient *redis.Client, chatID int64) (int, error) {
	n, err := redisClient.Get(ctx, bonusKey(chatID)).Int()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, err
	}
	return n, nil
}

// IssueCode generates a fresh pending claim code for a chat, overwriting
// any previous unclaimed code (only the most recently offered code is
// valid).
func IssueCode(ctx context.Context, redisClient *redis.Client, chatID int64) (string, error) {
	code, err := generateCode()
	if err != nil {
		return "", fmt.Errorf("generate reward code: %w", err)
	}
	if err := redisClient.Set(ctx, codeKey(chatID), code, codeTTL).Err(); err != nil {
		return "", fmt.Errorf("store reward code: %w", err)
	}
	return code, nil
}

func generateCode() (string, error) {
	b := make([]byte, codeLength)
	randomBytes := make([]byte, codeLength)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	for i, rb := range randomBytes {
		b[i] = codeCharset[int(rb)%len(codeCharset)]
	}
	return string(b), nil
}

type ClaimResult int

const (
	ClaimInvalidCode ClaimResult = iota
	ClaimLimitReached
	ClaimGranted
)

// Claim attempts to redeem a pending code for bonusAmount extra prompts,
// enforcing maxClaimsPerDay. On success the code is consumed (can't be
// reused) and the chat's cumulative daily bonus increases.
func Claim(ctx context.Context, redisClient *redis.Client, chatID int64, code string, bonusAmount, maxClaimsPerDay int) (ClaimResult, error) {
	stored, err := redisClient.Get(ctx, codeKey(chatID)).Result()
	if err != nil {
		if err == redis.Nil {
			return ClaimInvalidCode, nil
		}
		return ClaimInvalidCode, err
	}
	if !strings.EqualFold(strings.TrimSpace(stored), strings.TrimSpace(code)) {
		return ClaimInvalidCode, nil
	}

	claims, err := redisClient.Incr(ctx, claimsKey(chatID)).Result()
	if err != nil {
		return ClaimInvalidCode, err
	}
	if claims == 1 {
		redisClient.Expire(ctx, claimsKey(chatID), dayTTL)
	}
	if int(claims) > maxClaimsPerDay {
		return ClaimLimitReached, nil
	}

	// Consume the code so it can't be claimed twice.
	redisClient.Del(ctx, codeKey(chatID))

	newBonus, err := redisClient.IncrBy(ctx, bonusKey(chatID), int64(bonusAmount)).Result()
	if err != nil {
		return ClaimInvalidCode, err
	}
	if newBonus == int64(bonusAmount) {
		redisClient.Expire(ctx, bonusKey(chatID), dayTTL)
	}

	return ClaimGranted, nil
}
