package payment_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/config"
	"aura-saku/internal/db"
	"aura-saku/internal/models"
	"aura-saku/internal/service/payment"
)

// setup is an integration test helper against real Postgres/Redis instances
// (see docker-compose.yml), skipped automatically if either is unreachable,
// mirroring every other service's test setup in this project.
func setup(t *testing.T) (*payment.Service, *gorm.DB, *redis.Client, *models.User) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("skipping integration test, config load failed: %v", err)
	}
	gormDB, err := db.NewPostgres(cfg)
	if err != nil {
		t.Skipf("skipping integration test, postgres not reachable: %v", err)
	}
	if err := db.RunMigrations(gormDB); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	redisClient := redis.NewClient(&redis.Options{Addr: fmt.Sprintf("%s:%s", cfg.RedisHost, cfg.RedisPort)})
	if err := redisClient.Ping(context.Background()).Err(); err != nil {
		t.Skipf("skipping integration test, redis not reachable: %v", err)
	}

	user := &models.User{TelegramChatID: time.Now().UnixNano(), DefaultCurrency: "IDR", Tier: models.TierFree}
	if err := gormDB.Create(user).Error; err != nil {
		t.Fatalf("create test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", user.ID).Delete(&models.Subscription{})
		gormDB.Unscoped().Where("id = ?", user.ID).Delete(&models.User{})
		redisClient.Del(context.Background(), fmt.Sprintf("aura:paymentpending:%d", user.ID))
	})

	// Sandbox-config service — CreateCheckout's actual Midtrans network call
	// is exercised live against the real sandbox separately (see this
	// feature's plan doc), not in this automated suite. Tests here only
	// reach code paths that return before ever touching the network.
	svc := payment.NewService(gormDB, redisClient, cfg.MidtransServerKey, false, true)
	return svc, gormDB, redisClient, user
}

func TestIsConfigured(t *testing.T) {
	unconfiguredKey := payment.NewService(nil, nil, "", false, true)
	if unconfiguredKey.IsConfigured() {
		t.Error("IsConfigured() = true for an empty server key (active=true), want false")
	}

	configured := payment.NewService(nil, nil, "some-server-key", false, true)
	if !configured.IsConfigured() {
		t.Error("IsConfigured() = false for a non-empty server key with active=true, want true")
	}

	// The MIDTRANS_ACTIVE kill switch: a real key present but active=false
	// must still report unavailable — this is the whole point of having a
	// toggle independent of whether credentials are configured.
	inactive := payment.NewService(nil, nil, "some-server-key", false, false)
	if inactive.IsConfigured() {
		t.Error("IsConfigured() = true with active=false despite a valid server key, want false")
	}

	bothOff := payment.NewService(nil, nil, "", false, false)
	if bothOff.IsConfigured() {
		t.Error("IsConfigured() = true with neither a key nor active=true, want false")
	}
}

// TestCreateCheckout_AlreadyPendingRejectsWithoutNetworkCall covers the
// double-checkout guard: when a pending order_id is already cached for this
// user, CreateCheckout must refuse before ever calling Midtrans — verified
// here by the fact this test passes with no real Midtrans credentials
// configured at all (config.Load()'s MidtransServerKey may well be empty in
// this dev environment), since the guard short-circuits before any network
// call would be attempted.
func TestCreateCheckout_AlreadyPendingRejectsWithoutNetworkCall(t *testing.T) {
	svc, _, redisClient, user := setup(t)
	ctx := context.Background()

	if err := redisClient.Set(ctx, fmt.Sprintf("aura:paymentpending:%d", user.ID), "sub-existing-order", time.Hour).Err(); err != nil {
		t.Fatalf("seed pending checkout key: %v", err)
	}

	_, err := svc.CreateCheckout(ctx, user)
	if !errors.Is(err, apperror.ErrPaymentAlreadyPending) {
		t.Errorf("CreateCheckout err = %v, want ErrPaymentAlreadyPending", err)
	}
}

// TestExpireSubscriptions_RevertsOnlyActuallyExpired covers the daily
// expiry job's core correctness: an active subscription past its
// ExpiresAt is reverted (Subscription.Status -> expired, User.Tier ->
// free) and returned for notification, while an active, still-valid
// subscription for a different user is left completely untouched.
func TestExpireSubscriptions_RevertsOnlyActuallyExpired(t *testing.T) {
	svc, gormDB, _, expiredUser := setup(t)
	ctx := context.Background()

	validUser := &models.User{TelegramChatID: time.Now().UnixNano() + 1, DefaultCurrency: "IDR", Tier: models.TierPro}
	if err := gormDB.Create(validUser).Error; err != nil {
		t.Fatalf("create second test user: %v", err)
	}
	t.Cleanup(func() {
		gormDB.Unscoped().Where("user_id = ?", validUser.ID).Delete(&models.Subscription{})
		gormDB.Unscoped().Where("id = ?", validUser.ID).Delete(&models.User{})
	})

	if err := gormDB.Model(&models.User{}).Where("id = ?", expiredUser.ID).Update("tier", models.TierPro).Error; err != nil {
		t.Fatalf("set expiredUser to pro: %v", err)
	}

	past := time.Now().UTC().Add(-24 * time.Hour)
	if err := gormDB.Create(&models.Subscription{
		UserID: expiredUser.ID, Tier: models.TierPro, Status: models.SubStatusActive,
		StartedAt: past.Add(-30 * 24 * time.Hour), ExpiresAt: &past, PaymentRef: "sub-expired-order",
	}).Error; err != nil {
		t.Fatalf("seed expired subscription: %v", err)
	}

	future := time.Now().UTC().Add(24 * time.Hour)
	if err := gormDB.Create(&models.Subscription{
		UserID: validUser.ID, Tier: models.TierPro, Status: models.SubStatusActive,
		StartedAt: time.Now().UTC(), ExpiresAt: &future, PaymentRef: "sub-valid-order",
	}).Error; err != nil {
		t.Fatalf("seed still-valid subscription: %v", err)
	}

	affected, err := svc.ExpireSubscriptions(ctx)
	if err != nil {
		t.Fatalf("ExpireSubscriptions: %v", err)
	}
	// Scoped to these two test users rather than the exact returned slice —
	// this runs against a shared dev database that may hold other
	// subscriptions from manual sandbox testing.
	var gotExpiredUser, gotValidUser bool
	for _, id := range affected {
		if id == expiredUser.ID {
			gotExpiredUser = true
		}
		if id == validUser.ID {
			gotValidUser = true
		}
	}
	if !gotExpiredUser {
		t.Errorf("ExpireSubscriptions = %v, want it to include expiredUser %d", affected, expiredUser.ID)
	}
	if gotValidUser {
		t.Errorf("ExpireSubscriptions = %v, must not include validUser %d (its subscription hasn't expired)", affected, validUser.ID)
	}

	var reloadedExpiredUser models.User
	gormDB.First(&reloadedExpiredUser, expiredUser.ID)
	if reloadedExpiredUser.Tier != models.TierFree {
		t.Errorf("expiredUser.Tier = %q, want free", reloadedExpiredUser.Tier)
	}
	var expiredSub models.Subscription
	gormDB.Where("payment_ref = ?", "sub-expired-order").First(&expiredSub)
	if expiredSub.Status != models.SubStatusExpired {
		t.Errorf("expired subscription Status = %q, want expired", expiredSub.Status)
	}

	var reloadedValidUser models.User
	gormDB.First(&reloadedValidUser, validUser.ID)
	if reloadedValidUser.Tier != models.TierPro {
		t.Errorf("validUser.Tier = %q, want it to remain pro (untouched)", reloadedValidUser.Tier)
	}
	var validSub models.Subscription
	gormDB.Where("payment_ref = ?", "sub-valid-order").First(&validSub)
	if validSub.Status != models.SubStatusActive {
		t.Errorf("still-valid subscription Status = %q, want it to remain active (untouched)", validSub.Status)
	}
}

// TestExpireSubscriptions_NoneExpiredReturnsEmpty covers the common no-op
// tick for a specific still-valid subscription: it must never appear in the
// affected list. Scoped to this test's own user (rather than asserting the
// global result is empty) since this runs against a shared dev database
// that may already hold other subscriptions from manual sandbox testing.
func TestExpireSubscriptions_NoneExpiredReturnsEmpty(t *testing.T) {
	svc, gormDB, _, user := setup(t)
	ctx := context.Background()

	future := time.Now().UTC().Add(24 * time.Hour)
	if err := gormDB.Create(&models.Subscription{
		UserID: user.ID, Tier: models.TierPro, Status: models.SubStatusActive,
		StartedAt: time.Now().UTC(), ExpiresAt: &future, PaymentRef: "sub-not-expired-order",
	}).Error; err != nil {
		t.Fatalf("seed still-valid subscription: %v", err)
	}

	affected, err := svc.ExpireSubscriptions(ctx)
	if err != nil {
		t.Fatalf("ExpireSubscriptions: %v", err)
	}
	for _, id := range affected {
		if id == user.ID {
			t.Errorf("ExpireSubscriptions affected this test's user %d, want it untouched (its subscription isn't expired yet)", user.ID)
		}
	}
}
