// Package payment implements the Pro-tier checkout flow via Midtrans Snap:
// creating a checkout link, verifying/handling the payment webhook, and
// expiring subscriptions that weren't renewed. Telegram messaging is
// deliberately NOT done here — every method returns enough information for
// the caller (bot.Service) to decide what, if anything, to tell the user,
// the same "DB work in the service, chat side-effect in bot.Service" split
// already used by RunBudgetRolloverNudges/RunCategoryInsights.
package payment

import (
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/midtrans/midtrans-go/snap"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

// ProPriceIDR is the flat monthly Pro-tier price — a plain constant for now,
// matching how every other interval/limit in this project started (see
// e.g. report.categoryIncreaseThresholdPercent's own doc comment).
const ProPriceIDR int64 = 50_000

// subscriptionDuration is how long a successful payment grants Pro for —
// 30 days rather than a true calendar month, since it's measured from the
// moment payment clears, not from a fixed billing anchor date.
const subscriptionDuration = 30 * 24 * time.Hour

// pendingCheckoutTTL bounds how long CreateCheckout's Redis marker (and
// therefore HandleNotification's willingness to notify on a failed
// payment) stays valid — long enough for a real checkout session, short
// enough that an abandoned one doesn't block a retry indefinitely.
const pendingCheckoutTTL = 1 * time.Hour

type Service struct {
	db         *gorm.DB
	redis      *redis.Client
	snapClient snap.Client
	coreClient coreapi.Client
	configured bool
}

// NewService configures both Midtrans clients from serverKey directly
// (rather than the SDK's alternative global midtrans.ServerKey/Environment
// vars) so multiple *Service instances — e.g. in tests — never race on
// shared package state. An empty serverKey leaves the service unconfigured
// (see IsConfigured) — matches DashboardBaseURL's existing "empty disables
// this feature entirely" convention rather than failing to start.
func NewService(db *gorm.DB, redisClient *redis.Client, serverKey string, isProduction bool) *Service {
	env := midtrans.Sandbox
	if isProduction {
		env = midtrans.Production
	}

	s := &Service{db: db, redis: redisClient, configured: serverKey != ""}
	s.snapClient.New(serverKey, env)
	s.coreClient.New(serverKey, env)
	return s
}

// IsConfigured reports whether a Midtrans server key was actually supplied
// — bot.Service checks this before attempting a checkout, replying
// KeySubscribeUnavailable instead of a confusing Midtrans API error when
// it's false.
func (s *Service) IsConfigured() bool {
	return s.configured
}

// orderIDPattern extracts the user id embedded in an order_id built by
// buildOrderID — this lets HandleNotification identify which user a
// callback belongs to without a lookup table, including for the very FIRST
// notification about a given order_id, before any Subscription row exists
// to look up by PaymentRef.
var orderIDPattern = regexp.MustCompile(`^sub-(\d+)-`)

func buildOrderID(userID uint) string {
	return fmt.Sprintf("sub-%d-%s", userID, uuid.NewString())
}

func parseUserIDFromOrderID(orderID string) (uint, bool) {
	m := orderIDPattern.FindStringSubmatch(orderID)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(id), true
}

func pendingCheckoutKey(userID uint) string {
	return fmt.Sprintf("aura:paymentpending:%d", userID)
}

// CreateCheckout starts a new Snap payment for user, returning the URL to
// send them. Refuses (ErrPaymentAlreadyPending) if the user already has an
// unresolved checkout within pendingCheckoutTTL — never opens two
// simultaneous Snap transactions for the same user, which would otherwise
// let a stale link "succeed" a day after a newer one already did.
func (s *Service) CreateCheckout(ctx context.Context, user *models.User) (redirectURL string, err error) {
	if existing, err := s.redis.Get(ctx, pendingCheckoutKey(user.ID)).Result(); err == nil && existing != "" {
		return "", apperror.ErrPaymentAlreadyPending
	}

	orderID := buildOrderID(user.ID)
	req := &snap.Request{
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  orderID,
			GrossAmt: ProPriceIDR,
		},
		CustomerDetail: &midtrans.CustomerDetails{
			FName: user.FirstName,
		},
	}

	resp, merr := s.snapClient.CreateTransaction(req)
	if merr != nil {
		return "", fmt.Errorf("create midtrans snap transaction: %w", merr)
	}

	if err := s.redis.Set(ctx, pendingCheckoutKey(user.ID), orderID, pendingCheckoutTTL).Err(); err != nil {
		// The checkout itself already succeeded — losing the pending marker
		// only means a later failed-payment notification won't message the
		// user, not that the payment flow itself is broken. Not worth
		// failing the whole call over.
		return resp.RedirectURL, nil
	}

	return resp.RedirectURL, nil
}

// NotificationOutcome is HandleNotification's result — what bot.Service
// should tell the user, if anything.
type NotificationOutcome string

const (
	// OutcomeActivated means Pro is now active (or already was, for a
	// retried callback about the same order_id) — reply KeySubscribeSuccess
	// with ExpiresAt.
	OutcomeActivated NotificationOutcome = "activated"
	// OutcomePending means the payment is still awaiting completion — no
	// message, the user hasn't finished paying yet.
	OutcomePending NotificationOutcome = "pending"
	// OutcomeFailed means the payment was denied/canceled/expired AND it
	// was still the user's current pending checkout — reply
	// KeySubscribeFailed.
	OutcomeFailed NotificationOutcome = "failed"
	// OutcomeIgnored means nothing actionable: an order_id that doesn't
	// belong to this user's current pending checkout (stale/superseded), an
	// unrecognized order_id shape, or a status this pass doesn't act on
	// (e.g. refund) — no message.
	OutcomeIgnored NotificationOutcome = "ignored"
)

// NotificationResult tells the caller which user was affected and what
// happened, without this package ever touching Telegram itself.
type NotificationResult struct {
	UserID    uint
	Outcome   NotificationOutcome
	ExpiresAt time.Time // only meaningful when Outcome == OutcomeActivated
}

// HandleNotification verifies and processes one Midtrans payment
// notification callback body. Per Midtrans's own documented best practice,
// the webhook body's own transaction_status is never trusted directly for
// deciding whether to grant Pro — only the signature is checked against the
// body, and the actual status driving the outcome comes from a fresh
// CheckTransaction call against Midtrans's API.
func (s *Service) HandleNotification(ctx context.Context, body []byte) (*NotificationResult, error) {
	var notif coreapi.TransactionStatusResponse
	if err := json.Unmarshal(body, &notif); err != nil {
		return nil, fmt.Errorf("decode midtrans notification: %w", err)
	}

	if !validSignature(notif.OrderID, notif.StatusCode, notif.GrossAmount, s.snapClient.ServerKey, notif.SignatureKey) {
		return nil, apperror.ErrInvalidWebhookSignature
	}

	userID, ok := parseUserIDFromOrderID(notif.OrderID)
	if !ok {
		return &NotificationResult{Outcome: OutcomeIgnored}, nil
	}

	status, merr := s.coreClient.CheckTransaction(notif.OrderID)
	if merr != nil {
		return nil, fmt.Errorf("check midtrans transaction status: %w", merr)
	}

	switch status.TransactionStatus {
	case "capture":
		if status.FraudStatus != "accept" {
			return &NotificationResult{UserID: userID, Outcome: OutcomeIgnored}, nil
		}
		return s.activate(ctx, userID, notif.OrderID)
	case "settlement":
		return s.activate(ctx, userID, notif.OrderID)
	case "pending":
		return &NotificationResult{UserID: userID, Outcome: OutcomePending}, nil
	case "deny", "cancel", "expire", "failure":
		return s.handleFailure(ctx, userID, notif.OrderID)
	default:
		// refund/partial_refund/authorize and anything else future Midtrans
		// statuses might send — acknowledged but not acted on this pass
		// (see the plan's "deliberately out of scope" list).
		return &NotificationResult{UserID: userID, Outcome: OutcomeIgnored}, nil
	}
}

// validSignature recomputes Midtrans's documented signature formula —
// SHA512(order_id + status_code + gross_amount + ServerKey) — and compares
// it to the notification's own signature_key using a constant-time
// comparison, so a callback can't be forged (or the timing of the
// comparison itself exploited) by anyone who doesn't know the server key.
func validSignature(orderID, statusCode, grossAmount, serverKey, signatureKey string) bool {
	h := sha512.New()
	h.Write([]byte(orderID + statusCode + grossAmount + serverKey))
	computed := hex.EncodeToString(h.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(computed), []byte(signatureKey)) == 1
}

// activate is the settlement/capture+accept path: idempotently creates the
// Subscription row and flips User.Tier to Pro. A retried callback for an
// order_id already processed (Midtrans explicitly retries notifications)
// finds the existing row and reports the same OutcomeActivated without
// writing anything twice.
func (s *Service) activate(ctx context.Context, userID uint, orderID string) (*NotificationResult, error) {
	var existing models.Subscription
	err := s.db.WithContext(ctx).Where("payment_ref = ?", orderID).First(&existing).Error
	if err == nil {
		return &NotificationResult{UserID: userID, Outcome: OutcomeActivated, ExpiresAt: *existing.ExpiresAt}, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("look up existing subscription for %s: %w", orderID, err)
	}

	now := time.Now().UTC()
	expiresAt := now.Add(subscriptionDuration)
	sub := &models.Subscription{
		UserID:     userID,
		Tier:       models.TierPro,
		Status:     models.SubStatusActive,
		StartedAt:  now,
		ExpiresAt:  &expiresAt,
		PaymentRef: orderID,
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(sub).Error; err != nil {
			return fmt.Errorf("create subscription: %w", err)
		}
		if err := tx.Model(&models.User{}).Where("id = ?", userID).Update("tier", models.TierPro).Error; err != nil {
			return fmt.Errorf("set user tier to pro: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.redis.Del(ctx, pendingCheckoutKey(userID))
	return &NotificationResult{UserID: userID, Outcome: OutcomeActivated, ExpiresAt: expiresAt}, nil
}

// handleFailure only reports OutcomeFailed (worth telling the user) when
// orderID is still their CURRENT pending checkout — if they've since
// started a newer one, an old abandoned attempt failing shouldn't interrupt
// them with a "try again" message about a checkout they've already moved
// past.
func (s *Service) handleFailure(ctx context.Context, userID uint, orderID string) (*NotificationResult, error) {
	pending, err := s.redis.Get(ctx, pendingCheckoutKey(userID)).Result()
	if err != nil || pending != orderID {
		return &NotificationResult{UserID: userID, Outcome: OutcomeIgnored}, nil
	}
	s.redis.Del(ctx, pendingCheckoutKey(userID))
	return &NotificationResult{UserID: userID, Outcome: OutcomeFailed}, nil
}

// ExpireSubscriptions reverts every active-but-past-expiry subscription to
// free, returning the affected user ids so bot.Service can notify each one
// — the scheduler-job body for the daily "subscription-expiry" job.
func (s *Service) ExpireSubscriptions(ctx context.Context) ([]uint, error) {
	now := time.Now().UTC()

	var expired []models.Subscription
	if err := s.db.WithContext(ctx).
		Where("status = ? AND expires_at < ?", models.SubStatusActive, now).
		Find(&expired).Error; err != nil {
		return nil, fmt.Errorf("find expired subscriptions: %w", err)
	}
	if len(expired) == 0 {
		return nil, nil
	}

	userIDs := make([]uint, len(expired))
	subIDs := make([]uint, len(expired))
	for i, sub := range expired {
		userIDs[i] = sub.UserID
		subIDs[i] = sub.ID
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Subscription{}).Where("id IN ?", subIDs).Update("status", models.SubStatusExpired).Error; err != nil {
			return fmt.Errorf("mark subscriptions expired: %w", err)
		}
		if err := tx.Model(&models.User{}).Where("id IN ?", userIDs).Update("tier", models.TierFree).Error; err != nil {
			return fmt.Errorf("revert users to free tier: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return userIDs, nil
}
