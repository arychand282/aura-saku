// Package report computes transaction totals over a period — shared by the
// dashboard's report API (internal/api/handlers/report_handler.go) and the
// bot's natural-language spending-query intent (internal/service/bot), so
// both answer "how much have I spent" from the same query instead of two
// independently-maintained ones.
package report

import (
	"context"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
)

type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// CategoryTotal is one row of TopCategories' result.
type CategoryTotal struct {
	CategoryName string  `json:"category_name"`
	Total        float64 `json:"total"`
}

// PeriodRange resolves a period keyword into a [start, now] UTC range.
// "weekly" is a trailing 7 days, not a calendar week — matches the
// dashboard's existing PeriodSelector semantics.
func PeriodRange(period string) (start, end time.Time, err error) {
	now := time.Now().UTC()
	switch period {
	case "daily":
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), now, nil
	case "weekly":
		return now.AddDate(0, 0, -7), now, nil
	case "monthly":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC), now, nil
	case "yearly":
		return time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC), now, nil
	default:
		return time.Time{}, time.Time{}, apperror.ErrInvalidPeriod
	}
}

// Summary aggregates a user's transaction totals by type over the given period.
func (s *Service) Summary(ctx context.Context, userID uint, period string) (totals map[string]float64, start, end time.Time, err error) {
	start, end, err = PeriodRange(period)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}

	type totalRow struct {
		Type  string
		Total float64
	}
	var rows []totalRow
	if result := s.db.WithContext(ctx).Model(&models.Transaction{}).
		Select("type, COALESCE(SUM(converted_amount), 0) as total").
		Where("user_id = ? AND occurred_at BETWEEN ? AND ?", userID, start, end).
		Group("type").
		Scan(&rows); result.Error != nil {
		return nil, time.Time{}, time.Time{}, result.Error
	}

	totals = map[string]float64{}
	for _, r := range rows {
		totals[r.Type] = r.Total
	}
	return totals, start, end, nil
}

// TopCategories returns the top 5 expense categories by converted amount
// over the given period.
func (s *Service) TopCategories(ctx context.Context, userID uint, period string) ([]CategoryTotal, error) {
	start, end, err := PeriodRange(period)
	if err != nil {
		return nil, err
	}

	var rows []CategoryTotal
	result := s.db.WithContext(ctx).Table("transactions").
		Select("categories.name as category_name, COALESCE(SUM(transactions.converted_amount), 0) as total").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ? AND transactions.type = ? AND transactions.occurred_at BETWEEN ? AND ?", userID, models.TxExpense, start, end).
		Group("categories.name").
		Order("total DESC").
		Limit(5).
		Scan(&rows)
	return rows, result.Error
}

// CategoryTotal sums expense transactions in the given period whose category
// name matches categoryQuery (case-insensitive partial match, e.g. "food"
// matches a "Food" category). matched=false means no category exists
// matching categoryQuery at all — distinct from a real category that simply
// had zero spend this period — so callers (the bot's query-intent reply) can
// say "couldn't find that category" instead of silently reporting a
// misleading zero.
func (s *Service) CategoryTotal(ctx context.Context, userID uint, period, categoryQuery string) (total float64, matched bool, err error) {
	start, end, err := PeriodRange(period)
	if err != nil {
		return 0, false, err
	}

	like := "%" + categoryQuery + "%"

	var categoryCount int64
	if err := s.db.WithContext(ctx).Model(&models.Category{}).
		Where("name ILIKE ?", like).
		Count(&categoryCount).Error; err != nil {
		return 0, false, err
	}
	if categoryCount == 0 {
		return 0, false, nil
	}

	err = s.db.WithContext(ctx).Table("transactions").
		Select("COALESCE(SUM(transactions.converted_amount), 0)").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ? AND transactions.type = ? AND transactions.occurred_at BETWEEN ? AND ? AND categories.name ILIKE ?",
			userID, models.TxExpense, start, end, like).
		Scan(&total).Error
	return total, true, err
}
