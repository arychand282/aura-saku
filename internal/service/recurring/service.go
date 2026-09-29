// Package recurring owns scheduling for RecurringTransaction rows — a
// user-defined transaction (rent, a subscription, a salary) that should be
// auto-recorded once a month instead of requiring a fresh message every
// time. This package only knows about the schedule (create/cancel/list/
// which ones are due/advance the schedule); actually posting a due one as a
// real Transaction and notifying the user lives in
// bot.Service.RunRecurringTransactions (an internal/scheduler job), which
// needs transaction.Service's category-resolution/FX-conversion logic and
// the Messenger — neither of which belongs in this package.
package recurring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/ai"
	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Create schedules a new recurring transaction from parsed
// (ai.IntentRecurringCreate), computing its first NextRunAt as the next
// occurrence of parsed.DayOfMonth strictly AFTER now — never today, even if
// today happens to be that day of the month, so setting one up never
// doubles up with something the user already recorded normally this month.
func (s *Service) Create(ctx context.Context, userID uint, parsed *ai.ParsedTransaction) (*models.RecurringTransaction, error) {
	rt := &models.RecurringTransaction{
		UserID:       userID,
		Type:         models.TransactionType(parsed.Type),
		Amount:       parsed.Amount,
		Currency:     strings.ToUpper(parsed.Currency),
		CategoryName: parsed.CategoryName,
		Description:  parsed.Description,
		Bucket:       resolveBucket(parsed.Bucket),
		DayOfMonth:   parsed.DayOfMonth,
		NextRunAt:    nextOccurrenceAfter(time.Now().UTC(), parsed.DayOfMonth),
		IsActive:     true,
	}
	if err := s.db.WithContext(ctx).Create(rt).Error; err != nil {
		return nil, fmt.Errorf("insert recurring transaction: %w", err)
	}
	return rt, nil
}

// ListActive returns a user's active recurring transactions in creation
// order, for the /recurring list command.
func (s *Service) ListActive(ctx context.Context, userID uint) ([]*models.RecurringTransaction, error) {
	var rows []*models.RecurringTransaction
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ?", userID, true).
		Order("id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list active recurring transactions: %w", err)
	}
	return rows, nil
}

// Cancel deactivates (not soft-deletes — history stays queryable) the
// user's own recurring transaction with the given id. Scoped by user_id so
// one user can never cancel another's; ErrRecurringNotFound covers both
// "doesn't exist" and "exists but belongs to someone else" identically,
// the same ownership guarantee transaction.Service's correction/deletion
// targeting relies on.
func (s *Service) Cancel(ctx context.Context, userID, id uint) (*models.RecurringTransaction, error) {
	var rt models.RecurringTransaction
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND id = ? AND is_active = ?", userID, id, true).
		First(&rt).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrRecurringNotFound
		}
		return nil, fmt.Errorf("find recurring transaction %d: %w", id, err)
	}

	if err := s.db.WithContext(ctx).Model(&rt).Update("is_active", false).Error; err != nil {
		return nil, fmt.Errorf("deactivate recurring transaction %d: %w", id, err)
	}
	rt.IsActive = false
	return &rt, nil
}

// Due returns every active recurring transaction whose NextRunAt has
// arrived, User preloaded so a caller can message the right chat without a
// second per-row query. Scans across ALL users — the only caller
// (bot.Service.RunRecurringTransactions) is the scheduled job itself, not a
// per-user request.
func (s *Service) Due(ctx context.Context, now time.Time) ([]*models.RecurringTransaction, error) {
	var rows []*models.RecurringTransaction
	err := s.db.WithContext(ctx).
		Preload("User").
		Where("is_active = ? AND next_run_at <= ?", true, now).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list due recurring transactions: %w", err)
	}
	return rows, nil
}

// Advance moves rt's schedule forward by exactly one calendar month from
// its OWN current NextRunAt (not from "now") — so a fixed day-of-month
// schedule never drifts even if the scheduler tick that caught this run was
// hours or days late.
func (s *Service) Advance(ctx context.Context, rt *models.RecurringTransaction) error {
	next := rt.NextRunAt.AddDate(0, 1, 0)
	if err := s.db.WithContext(ctx).Model(rt).Update("next_run_at", next).Error; err != nil {
		return fmt.Errorf("advance recurring transaction %d: %w", rt.ID, err)
	}
	rt.NextRunAt = next
	return nil
}

// lastDayOfMonth returns how many days the given month has.
func lastDayOfMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// occurrenceIn returns dayOfMonth's actual date within (year, month) at UTC
// midnight, clamped to that month's own last day if dayOfMonth overshoots
// it (e.g. 31 in April, or 29-31 in February) — Go's time.Date normalizes a
// month argument outside 1-12 on its own (rolling the year forward/back
// correctly), so passing month+1 or month-1 here is always safe even across
// a December/January boundary.
func occurrenceIn(year int, month time.Month, dayOfMonth int) time.Time {
	day := dayOfMonth
	if last := lastDayOfMonth(year, month); day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// nextOccurrenceAfter returns the next occurrence of dayOfMonth strictly
// AFTER from — this month's if it hasn't happened yet, otherwise next
// month's. "Strictly after" (not "on or after") is deliberate: creating a
// recurring transaction ON its own day-of-month must never immediately
// count as due, or a scheduler tick minutes later would double-record
// something the user is still just setting up.
func nextOccurrenceAfter(from time.Time, dayOfMonth int) time.Time {
	candidate := occurrenceIn(from.Year(), from.Month(), dayOfMonth)
	if !candidate.After(from) {
		candidate = occurrenceIn(from.Year(), from.Month()+1, dayOfMonth)
	}
	return candidate
}

// resolveBucket mirrors transaction.Service's own unexported helper of the
// same purpose — kept as its own small copy rather than an exported shared
// helper, since three lines of mapping logic isn't worth a cross-package
// dependency for.
func resolveBucket(raw string) *models.BudgetBucket {
	b := models.BudgetBucket(strings.ToLower(strings.TrimSpace(raw)))
	switch b {
	case models.BucketNeeds, models.BucketWants, models.BucketSavings, models.BucketNone:
		return &b
	default:
		return nil
	}
}
