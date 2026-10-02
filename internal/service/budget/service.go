package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/i18n"
	"aura-saku/internal/models"
)

// percentSumTolerance absorbs float rounding (e.g. three thirds entered as
// 33.33/33.33/33.34) without rejecting an otherwise-valid allocation over a
// fraction of a percentage point.
const percentSumTolerance = 0.01

// Status is the shape returned by EvaluateBudget — the post-entry feedback
// shown to the user and surfaced on the dashboard.
type Status struct {
	NeedsSpent   float64  `json:"needs_spent"`
	NeedsLimit   float64  `json:"needs_limit"`
	WantsSpent   float64  `json:"wants_spent"`
	WantsLimit   float64  `json:"wants_limit"`
	SavingsSpent float64  `json:"savings_spent"`
	SavingsLimit float64  `json:"savings_limit"`
	Warnings     []string `json:"warnings"`
}

// Service owns the 50/30/20 budget guardrail logic: post-entry feedback,
// anomaly/impulse detection, and end-of-month rollover suggestions.
type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// EvaluateBudget computes the user's current calendar-month spend against
// their configured Needs/Wants/Savings allocation of this period's income,
// and flags anomalies: over-allocation, and a Wants spending pace projected
// to overshoot by month end.
func (s *Service) EvaluateBudget(ctx context.Context, userID uint, lang models.Language) (*Status, error) {
	b, err := s.getOrCreateActiveBudget(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("resolve active budget: %w", err)
	}

	now := time.Now().UTC()
	periodStart, periodEnd, err := s.resolvePeriod(ctx, userID, now)
	if err != nil {
		return nil, fmt.Errorf("resolve budget period: %w", err)
	}

	income, err := s.sumByType(ctx, userID, models.TxIncome, periodStart, now)
	if err != nil {
		return nil, fmt.Errorf("sum period income: %w", err)
	}

	spendByBucket, err := s.sumSpendByBucket(ctx, userID, periodStart, now)
	if err != nil {
		return nil, fmt.Errorf("sum spend by bucket: %w", err)
	}

	status := &Status{
		NeedsSpent:   spendByBucket[models.BucketNeeds],
		WantsSpent:   spendByBucket[models.BucketWants],
		SavingsSpent: spendByBucket[models.BucketSavings],
		NeedsLimit:   income * b.NeedsPercent / 100,
		WantsLimit:   income * b.WantsPercent / 100,
		SavingsLimit: income * b.SavingsPercent / 100,
	}

	if income <= 0 {
		status.Warnings = append(status.Warnings, i18n.T(lang, i18n.KeyBudgetNoIncome))
		return status, nil
	}

	if status.NeedsSpent > status.NeedsLimit {
		status.Warnings = append(status.Warnings, i18n.T(lang, i18n.KeyBudgetNeedsExceeded,
			b.NeedsPercent, status.NeedsSpent, status.NeedsLimit))
	}

	if status.WantsSpent > status.WantsLimit {
		status.Warnings = append(status.Warnings, i18n.T(lang, i18n.KeyBudgetWantsExceeded,
			b.WantsPercent, status.WantsSpent, status.WantsLimit))
	} else if status.WantsLimit > 0 {
		// Impulse/anomaly detector: project this period's Wants spend at the
		// current daily pace and warn if it's on track to blow past the
		// allocation, even though it hasn't yet. Uses the ACTUAL resolved
		// period (periodStart/periodEnd — a payday-to-payday cycle for a
		// user with recurring income configured, otherwise the calendar
		// month), not hardcoded "day of month"/"days in this calendar
		// month" math, which would silently drift once periodStart stopped
		// being the 1st.
		daysElapsed := now.Sub(periodStart).Hours()/24 + 1
		daysInPeriod := periodEnd.Sub(periodStart).Hours() / 24
		projected := status.WantsSpent / daysElapsed * daysInPeriod
		if projected > status.WantsLimit*1.2 {
			status.Warnings = append(status.Warnings, i18n.T(lang, i18n.KeyBudgetWantsPace, projected, status.WantsLimit))
		}
	}

	return status, nil
}

// LastMonthSurplus computes last calendar month's income-minus-expense net
// figure — shared by SuggestRollover's proactive nudge (below) and the
// on-demand "surplusku bulan lalu berapa?" natural-language query (see
// ai.IntentSurplus / bot.Service.handleSurplusQuery). Unlike SuggestRollover,
// this always returns the raw number, including zero or negative — a caller
// answering a direct question needs to say something even when there's no
// surplus, not silently skip the way the proactive nudge does.
func (s *Service) LastMonthSurplus(ctx context.Context, userID uint) (float64, error) {
	now := time.Now().UTC()
	prevMonthStart := startOfMonth(now.AddDate(0, -1, 0))
	thisMonthStart := startOfMonth(now)

	income, err := s.sumByType(ctx, userID, models.TxIncome, prevMonthStart, thisMonthStart)
	if err != nil {
		return 0, fmt.Errorf("sum previous period income: %w", err)
	}
	expense, err := s.sumByType(ctx, userID, models.TxExpense, prevMonthStart, thisMonthStart)
	if err != nil {
		return 0, fmt.Errorf("sum previous period expense: %w", err)
	}
	return income - expense, nil
}

// SuggestRollover proposes shifting last calendar month's income-minus-expense
// surplus into savings. Returns an empty string (no error) when there was no
// surplus to suggest rolling over.
func (s *Service) SuggestRollover(ctx context.Context, userID uint, defaultCurrency string, lang models.Language) (string, error) {
	surplus, err := s.LastMonthSurplus(ctx, userID)
	if err != nil {
		return "", err
	}
	if surplus <= 0 {
		return "", nil
	}

	return i18n.T(lang, i18n.KeyBudgetRollover, i18n.FormatMoney(lang, surplus, defaultCurrency)), nil
}

// CurrentBudget returns the user's active budget allocation, auto-creating
// the default 50/30/20 split on first use — same lookup EvaluateBudget
// itself relies on, exported so the bot's /budget (no arguments) command
// can show it without duplicating the get-or-create logic.
func (s *Service) CurrentBudget(ctx context.Context, userID uint) (*models.Budget, error) {
	return s.getOrCreateActiveBudget(ctx, userID)
}

// SetBudget replaces the user's active allocation with new percentages,
// effective immediately for the current calendar month's evaluation. The
// three percentages must each be non-negative and sum to 100 (within
// percentSumTolerance) — internal/models.Budget's own doc comment notes
// the DB layer deliberately doesn't enforce this via a CHECK constraint, so
// this is the one place that validation actually happens.
//
// The previous active budget (if any) is deactivated rather than deleted
// or updated in place, preserving a history of allocation changes over
// time rather than silently overwriting it — mirrors how a correction
// replaces a transaction (undo the old, create the new) rather than
// mutating it.
func (s *Service) SetBudget(ctx context.Context, userID uint, needsPercent, wantsPercent, savingsPercent float64) (*models.Budget, error) {
	if needsPercent < 0 || wantsPercent < 0 || savingsPercent < 0 {
		return nil, apperror.ErrInvalidBudgetPercentages
	}
	if math.Abs(needsPercent+wantsPercent+savingsPercent-100) > percentSumTolerance {
		return nil, apperror.ErrInvalidBudgetPercentages
	}

	newBudget := &models.Budget{
		UserID:         userID,
		NeedsPercent:   needsPercent,
		WantsPercent:   wantsPercent,
		SavingsPercent: savingsPercent,
		PeriodStart:    startOfMonth(time.Now().UTC()),
		IsActive:       true,
	}

	err := s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		if err := txDB.Model(&models.Budget{}).
			Where("user_id = ? AND is_active = ?", userID, true).
			Update("is_active", false).Error; err != nil {
			return fmt.Errorf("deactivate previous budget: %w", err)
		}
		if err := txDB.Create(newBudget).Error; err != nil {
			return fmt.Errorf("insert new budget: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newBudget, nil
}

// SavingsSince sums everything that counts as "savings" from start to now —
// the exact same definition EvaluateBudget's Needs/Wants/Savings split
// already uses (expense transactions explicitly bucketed "savings", plus
// the full amount of every investment-type transaction), via the same
// sumSpendByBucket this package already relies on internally. Exported so
// internal/service/savingsgoal.Service.Progress can track a goal against
// real savings activity without duplicating this aggregation or inventing
// a second, possibly-inconsistent definition of "savings".
func (s *Service) SavingsSince(ctx context.Context, userID uint, start time.Time) (float64, error) {
	byBucket, err := s.sumSpendByBucket(ctx, userID, start, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("sum savings since %s: %w", start.Format("2006-01-02"), err)
	}
	return byBucket[models.BucketSavings], nil
}

// resolvePeriod anchors a budget evaluation period to the user's own
// recurring income schedule's DayOfMonth when one exists (e.g. a recurring
// transaction created from "gaji tiap tanggal 30") — the most recent
// occurrence of that day on or before now, through the next one — rather
// than always the calendar month's 1st through the 1st of next month.
//
// Fixes a real reported confusion: a payday near the end of a month (e.g.
// the 30th) funds the NEXT roughly-30-day stretch of spending, but a strict
// "1st of this calendar month" window excluded it entirely the moment the
// calendar flipped to the next month, triggering a false "no income
// recorded" warning the day after a perfectly normal end-of-month payday.
// Falls back to the plain calendar month for a user with no recurring
// income configured — unchanged from the original behavior. If a user has
// more than one active recurring income schedule, the oldest (lowest id)
// one wins — picking a single, stable anchor matters more here than
// handling multiple income schedules, which is out of scope for this fix.
func (s *Service) resolvePeriod(ctx context.Context, userID uint, now time.Time) (start, end time.Time, err error) {
	var incomeTx models.RecurringTransaction
	dbErr := s.db.WithContext(ctx).
		Where("user_id = ? AND type = ? AND is_active = ?", userID, models.TxIncome, true).
		Order("id ASC").
		First(&incomeTx).Error
	if dbErr != nil {
		if errors.Is(dbErr, gorm.ErrRecordNotFound) {
			start = startOfMonth(now)
			return start, start.AddDate(0, 1, 0), nil
		}
		return time.Time{}, time.Time{}, fmt.Errorf("find active recurring income: %w", dbErr)
	}

	start = mostRecentPayday(now, incomeTx.DayOfMonth)
	end = occurrenceIn(start.Year(), start.Month()+1, incomeTx.DayOfMonth)
	return start, end, nil
}

// lastDayOfMonth/occurrenceIn mirror recurring.Service's own identical
// helpers — kept as their own small copy here rather than a cross-package
// dependency for a few lines of date math, the same convention
// recurring.Service's own resolveBucket copy already follows.
func lastDayOfMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func occurrenceIn(year int, month time.Month, dayOfMonth int) time.Time {
	day := dayOfMonth
	if last := lastDayOfMonth(year, month); day > last {
		day = last
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// mostRecentPayday returns the most recent occurrence of dayOfMonth ON OR
// BEFORE now — this month's if it's already happened, otherwise last
// month's. The inverse of recurring.Service's nextOccurrenceAfter (which
// finds the next FUTURE occurrence); resolvePeriod instead needs whichever
// payday most recently funded the user's current spending, not an
// upcoming one.
func mostRecentPayday(now time.Time, dayOfMonth int) time.Time {
	candidate := occurrenceIn(now.Year(), now.Month(), dayOfMonth)
	if candidate.After(now) {
		candidate = occurrenceIn(now.Year(), now.Month()-1, dayOfMonth)
	}
	return candidate
}

func (s *Service) getOrCreateActiveBudget(ctx context.Context, userID uint) (*models.Budget, error) {
	var b models.Budget
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ?", userID, true).
		Order("period_start DESC").
		First(&b).Error
	if err == nil {
		return &b, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	b = models.Budget{
		UserID:         userID,
		NeedsPercent:   50,
		WantsPercent:   30,
		SavingsPercent: 20,
		PeriodStart:    startOfMonth(time.Now().UTC()),
		IsActive:       true,
	}
	if err := s.db.WithContext(ctx).Create(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Service) sumByType(ctx context.Context, userID uint, txType models.TransactionType, start, end time.Time) (float64, error) {
	var total float64
	err := s.db.WithContext(ctx).Model(&models.Transaction{}).
		Select("COALESCE(SUM(converted_amount), 0)").
		Where("user_id = ? AND type = ? AND occurred_at BETWEEN ? AND ?", userID, txType, start, end).
		Scan(&total).Error
	return total, err
}

// sumSpendByBucket buckets this period's spend for the 50/30/20 comparison:
// Needs/Wants come from expense transactions, preferring each transaction's
// own AI-classified Bucket override (see models.Transaction.Bucket) over its
// category's static default bucket when set — e.g. a "shopping" purchase the
// AI classified as needs (an urgent replacement) counts toward Needs even
// though the shopping category itself defaults to Wants. Savings
// additionally includes the full amount of any investment-type transaction
// regardless of category, since the transaction Type itself already signals
// savings/investment intent.
func (s *Service) sumSpendByBucket(ctx context.Context, userID uint, start, end time.Time) (map[models.BudgetBucket]float64, error) {
	const bucketExpr = "COALESCE(transactions.bucket, categories.bucket, 'none')"

	type row struct {
		Bucket string
		Total  float64
	}
	var rows []row
	err := s.db.WithContext(ctx).Table("transactions").
		Select(bucketExpr+" as bucket, COALESCE(SUM(transactions.converted_amount), 0) as total").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ? AND transactions.type = ? AND transactions.occurred_at BETWEEN ? AND ?",
			userID, models.TxExpense, start, end).
		Group(bucketExpr).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := map[models.BudgetBucket]float64{}
	for _, r := range rows {
		result[models.BudgetBucket(r.Bucket)] += r.Total
	}

	investmentTotal, err := s.sumByType(ctx, userID, models.TxInvestment, start, end)
	if err != nil {
		return nil, err
	}
	result[models.BucketSavings] += investmentTotal

	return result, nil
}

func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
