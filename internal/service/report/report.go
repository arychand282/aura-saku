// Package report computes transaction totals over a period — shared by the
// dashboard's report API (internal/api/handlers/report_handler.go) and the
// bot's natural-language spending-query intent (internal/service/bot), so
// both answer "how much have I spent" from the same query instead of two
// independently-maintained ones.
package report

import (
	"context"
	"fmt"
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
	totals, err = s.SummaryRange(ctx, userID, start, end)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	return totals, start, end, nil
}

// SummaryRange is Summary's explicit-[start,end] counterpart — lets a caller
// (bot.Service.resolveQueryRange) override PeriodRange's default start
// boundary with a specific date stated in the message (e.g. "mulai dari
// bulan lalu") while still ending at "now", which PeriodRange itself has no
// way to express.
func (s *Service) SummaryRange(ctx context.Context, userID uint, start, end time.Time) (map[string]float64, error) {
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
		return nil, result.Error
	}

	totals := map[string]float64{}
	for _, r := range rows {
		totals[r.Type] = r.Total
	}
	return totals, nil
}

// TopCategories returns the top 5 expense categories by converted amount
// over the given period.
func (s *Service) TopCategories(ctx context.Context, userID uint, period string) ([]CategoryTotal, error) {
	start, end, err := PeriodRange(period)
	if err != nil {
		return nil, err
	}
	return s.TopCategoriesRange(ctx, userID, start, end)
}

// TopCategoriesRange is TopCategories' explicit-[start,end] counterpart —
// see SummaryRange's doc comment for why this exists.
func (s *Service) TopCategoriesRange(ctx context.Context, userID uint, start, end time.Time) ([]CategoryTotal, error) {
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

// startOfMonth returns the UTC midnight that starts t's calendar month.
// Mirrors budget.Service's own helper of the same name — kept as its own
// one-line copy here rather than a cross-package dependency for something
// this small, the same call already made for recurring.Service's
// resolveBucket.
func startOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// topCategoriesLimit and categoryIncreaseThresholdPercent tune
// MonthlyCategoryInsight below: how many of last month's top categories to
// surface, and how much a category must have grown since the month before
// to be worth calling out — a smaller/unset threshold would flag ordinary
// month-to-month noise every single time.
const (
	topCategoriesLimit               = 3
	categoryIncreaseThresholdPercent = 15.0
)

// CategoryInsight is MonthlyCategoryInsight's result: last month's top
// expense categories, plus whichever one (if any) grew enough since the
// month before to be worth flagging. IncreasedCategory empty means no
// category cleared categoryIncreaseThresholdPercent, or there was no prior
// month to compare against at all.
type CategoryInsight struct {
	TotalExpense  float64
	TopCategories []CategoryTotal

	IncreasedCategory string
	// IncreasedLastMonth/IncreasedPriorMonth are both kept as real totals
	// (not just their delta) so a caller can show a direct side-by-side
	// comparison — "Rp600.000 last month vs Rp200.000 the month before" —
	// not just a bare percentage.
	IncreasedLastMonth  float64
	IncreasedPriorMonth float64
	IncreasedPercent    float64
}

// CategoryBreakdown sums expense transactions by category over the given
// calendar range — the same GORM-builder shape as TopCategories, but an
// explicit [start, end) range instead of PeriodRange's trailing-window
// period keywords (so a caller can align two adjacent calendar months
// against each other) and no row limit, since the caller needs the full
// set to do that alignment. Uses a half-open range (>= start AND < end,
// not BETWEEN's inclusive-inclusive) specifically so two back-to-back
// calls for adjacent months never double-count a transaction that landed
// exactly on the boundary between them.
func (s *Service) CategoryBreakdown(ctx context.Context, userID uint, start, end time.Time) ([]CategoryTotal, error) {
	var rows []CategoryTotal
	result := s.db.WithContext(ctx).Table("transactions").
		Select("categories.name as category_name, COALESCE(SUM(transactions.converted_amount), 0) as total").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ? AND transactions.type = ? AND transactions.occurred_at >= ? AND transactions.occurred_at < ?", userID, models.TxExpense, start, end).
		Group("categories.name").
		Order("total DESC").
		Scan(&rows)
	return rows, result.Error
}

// MonthlyCategoryInsight computes last calendar month's top expense
// categories and flags whichever one grew the most since the month
// before, for the bot's proactive end-of-month recap (see
// bot.Service.RunCategoryInsights). Returns (nil, nil) — not an error —
// when there was no expense data at all last month, mirroring
// budget.Service.SuggestRollover's "empty means nothing to report"
// convention.
func (s *Service) MonthlyCategoryInsight(ctx context.Context, userID uint) (*CategoryInsight, error) {
	now := time.Now().UTC()
	thisMonthStart := startOfMonth(now)
	lastMonthStart := startOfMonth(now.AddDate(0, -1, 0))
	twoMonthsAgoStart := startOfMonth(now.AddDate(0, -2, 0))

	lastMonth, err := s.CategoryBreakdown(ctx, userID, lastMonthStart, thisMonthStart)
	if err != nil {
		return nil, fmt.Errorf("category breakdown for last month: %w", err)
	}
	if len(lastMonth) == 0 {
		return nil, nil
	}

	priorMonth, err := s.CategoryBreakdown(ctx, userID, twoMonthsAgoStart, lastMonthStart)
	if err != nil {
		return nil, fmt.Errorf("category breakdown for the month before last: %w", err)
	}
	priorByName := make(map[string]float64, len(priorMonth))
	for _, row := range priorMonth {
		priorByName[row.CategoryName] = row.Total
	}

	insight := &CategoryInsight{}
	limit := topCategoriesLimit
	if limit > len(lastMonth) {
		limit = len(lastMonth)
	}
	insight.TopCategories = lastMonth[:limit]
	for _, row := range lastMonth {
		insight.TotalExpense += row.Total
	}

	// Find the single category with the largest absolute increase over its
	// prior-month amount, among every category (not just the top 3) —
	// something further down the list could still have grown the most
	// even if it isn't among last month's biggest categories in absolute
	// terms.
	var bestIncrease float64
	for _, row := range lastMonth {
		prior, hadPrior := priorByName[row.CategoryName]
		if !hadPrior || prior <= 0 {
			// No baseline to compare against — brand new spending in this
			// category isn't a meaningful "increase" to flag.
			continue
		}
		increase := row.Total - prior
		percent := increase / prior * 100
		if percent < categoryIncreaseThresholdPercent {
			continue
		}
		if increase > bestIncrease {
			bestIncrease = increase
			insight.IncreasedCategory = row.CategoryName
			insight.IncreasedLastMonth = row.Total
			insight.IncreasedPriorMonth = prior
			insight.IncreasedPercent = percent
		}
	}

	return insight, nil
}

// BucketBreakdown is BucketSpending's result: a budget bucket's total spend
// over the period, plus a per-category breakdown within it — used to answer
// "how much did I spend on Wants" with the same level of detail a budget
// warning already flagged, not just a single number.
type BucketBreakdown struct {
	Total      float64
	Categories []CategoryTotal
}

// BucketSpending sums expense transactions within a single Needs/Wants/
// Savings bucket over the given period, broken down by category — the
// bucket-scoped counterpart to CategoryTotal. Resolves each transaction's
// bucket the same way budget.Service.sumSpendByBucket does: its own
// AI-classified override first, falling back to its category's static
// default bucket (kept as its own copy of that expression here rather than
// a cross-package dependency for one SQL fragment, same reasoning as
// startOfMonth above).
func (s *Service) BucketSpending(ctx context.Context, userID uint, period string, bucket models.BudgetBucket) (*BucketBreakdown, error) {
	start, end, err := PeriodRange(period)
	if err != nil {
		return nil, err
	}
	return s.BucketSpendingRange(ctx, userID, start, end, bucket)
}

// BucketSpendingRange is BucketSpending's explicit-[start,end] counterpart —
// see SummaryRange's doc comment for why this exists.
func (s *Service) BucketSpendingRange(ctx context.Context, userID uint, start, end time.Time, bucket models.BudgetBucket) (*BucketBreakdown, error) {
	const bucketExpr = "COALESCE(transactions.bucket, categories.bucket, 'none')"

	var rows []CategoryTotal
	result := s.db.WithContext(ctx).Table("transactions").
		Select("categories.name as category_name, COALESCE(SUM(transactions.converted_amount), 0) as total").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where("transactions.user_id = ? AND transactions.type = ? AND transactions.occurred_at BETWEEN ? AND ? AND "+bucketExpr+" = ?",
			userID, models.TxExpense, start, end, string(bucket)).
		Group("categories.name").
		Order("total DESC").
		Scan(&rows)
	if result.Error != nil {
		return nil, result.Error
	}

	breakdown := &BucketBreakdown{Categories: rows}
	for _, row := range rows {
		breakdown.Total += row.Total
	}
	return breakdown, nil
}

// transactionListPageSize caps a single reply to ListTransactions' caller at
// this many items — a user asking "what did I buy for X" gets the 10 most
// recent, with the total match count telling the caller how many more exist
// for a "+N more, say 'show more' to continue" note, rather than dumping a
// potentially huge list in one message.
const transactionListPageSize = 10

// TransactionListItem is one row of ListTransactions' result — the same
// flat, JOIN-flattened shape as the dashboard's TransactionRow
// (internal/api/handlers/transaction_query.go), duplicated here rather than
// imported since a service package importing an api/handlers package would
// be a layering inversion neither side needs otherwise. Type is only worth
// showing in a reply when a list spans more than one transaction type (see
// ListTransactions' txType param) — a single-type list already says so in
// its own header and would be redundant repeating it on every line.
type TransactionListItem struct {
	OccurredAt   time.Time
	Type         models.TransactionType
	CategoryName string
	Description  string
	Amount       float64
}

// TransactionListPage is one page of ListTransactions' paginated result —
// Total is the full match count across every page, not just len(Items), so
// a caller can compute how many more remain beyond this page.
type TransactionListPage struct {
	Items []TransactionListItem
	Total int
}

// ListTransactions returns one page of a user's transactions within
// [start, end), most-recent-first — the data behind a natural-language
// "what did I buy for X" / "list my Y expenses" / "list semua transaksi"
// listing request, as opposed to query_spending's aggregate total/breakdown
// modes above. txType restricts to one transaction type ("" lists every
// type — e.g. for an explicit "list ALL transactions" request; callers
// defaulting to spending-only behavior should pass models.TxExpense
// explicitly, not rely on an implicit default here). At most one of
// uncategorizedOnly (Transaction.CategoryID IS NULL — e.g. a /sync "blind
// spot" adjustment, see categoryDisplayName's own doc comment), categoryQuery
// (ILIKE match, same convention as CategoryTotal), bucket (same COALESCE
// resolution as BucketSpending), or searchTerm (ILIKE match against
// Description OR RawInput — a specific merchant/place/item/person named by
// name, e.g. "starbucks", as opposed to categoryQuery's general budget
// category) should be set, checked in that order — leave all four
// empty/zero to list every matching transaction in the period regardless of
// category. offset is the caller-tracked pagination cursor (see
// bot.Service's short-lived Redis cursor); this always returns at most
// transactionListPageSize items starting from it, ordered with the row id as
// a tiebreaker so pagination stays stable even when two transactions share
// the exact same OccurredAt.
func (s *Service) ListTransactions(ctx context.Context, userID uint, start, end time.Time, txType models.TransactionType, categoryQuery string, bucket models.BudgetBucket, uncategorizedOnly bool, searchTerm string, offset int) (*TransactionListPage, error) {
	where := "transactions.user_id = ? AND transactions.occurred_at >= ? AND transactions.occurred_at < ?"
	args := []interface{}{userID, start, end}

	if txType != "" {
		where += " AND transactions.type = ?"
		args = append(args, txType)
	}

	switch {
	case uncategorizedOnly:
		where += " AND transactions.category_id IS NULL"
	case categoryQuery != "":
		where += " AND categories.name ILIKE ?"
		args = append(args, "%"+categoryQuery+"%")
	case bucket != "":
		where += " AND COALESCE(transactions.bucket, categories.bucket, 'none') = ?"
		args = append(args, string(bucket))
	case searchTerm != "":
		where += " AND (transactions.description ILIKE ? OR transactions.raw_input ILIKE ?)"
		args = append(args, "%"+searchTerm+"%", "%"+searchTerm+"%")
	}

	var total int64
	if err := s.db.WithContext(ctx).Table("transactions").
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where(where, args...).
		Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count transactions for listing: %w", err)
	}

	var items []TransactionListItem
	if err := s.db.WithContext(ctx).Table("transactions").
		Select(`transactions.occurred_at, transactions.type, COALESCE(categories.name, '') as category_name,
			transactions.description, transactions.converted_amount as amount`).
		Joins("LEFT JOIN categories ON categories.id = transactions.category_id").
		Where(where, args...).
		Order("transactions.occurred_at DESC, transactions.id DESC").
		Offset(offset).
		Limit(transactionListPageSize).
		Scan(&items).Error; err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}

	return &TransactionListPage{Items: items, Total: int(total)}, nil
}

// UncategorizedTotal sums expense transactions in the given period with no
// category assigned at all (Transaction.CategoryID nil) — the total-query
// counterpart to ListTransactions' uncategorizedOnly filter, for a
// follow-up naming whatever categoryDisplayName's fallback label was shown
// as (e.g. "how much was uncategorized this month").
func (s *Service) UncategorizedTotal(ctx context.Context, userID uint, period string) (float64, error) {
	start, end, err := PeriodRange(period)
	if err != nil {
		return 0, err
	}
	return s.UncategorizedTotalRange(ctx, userID, start, end)
}

// UncategorizedTotalRange is UncategorizedTotal's explicit-[start,end]
// counterpart — see SummaryRange's doc comment for why this exists.
func (s *Service) UncategorizedTotalRange(ctx context.Context, userID uint, start, end time.Time) (float64, error) {
	var total float64
	err := s.db.WithContext(ctx).Model(&models.Transaction{}).
		Select("COALESCE(SUM(converted_amount), 0)").
		Where("user_id = ? AND type = ? AND occurred_at BETWEEN ? AND ? AND category_id IS NULL",
			userID, models.TxExpense, start, end).
		Scan(&total).Error
	return total, err
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
	return s.CategoryTotalRange(ctx, userID, start, end, categoryQuery)
}

// CategoryTotalRange is CategoryTotal's explicit-[start,end] counterpart —
// see SummaryRange's doc comment for why this exists.
func (s *Service) CategoryTotalRange(ctx context.Context, userID uint, start, end time.Time, categoryQuery string) (total float64, matched bool, err error) {
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
