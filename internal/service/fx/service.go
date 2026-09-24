package fx

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Service resolves an exchange rate between two ISO 4217 currency codes, as
// of a given point in time (the transaction's OccurredAt), so multi-currency
// aggregation reflects the rate at the time money actually moved rather than
// whatever the rate happens to be when the report is generated.
type Service interface {
	GetExchangeRate(ctx context.Context, from, to string, at time.Time) (float64, error)
}

// StaticService is a fixed dummy-rate table, ignoring `at` since it has no
// concept of history. Used as LiveService's fallback so a live-provider
// outage never blocks recording a transaction.
type StaticService struct {
	ratesToUSD map[string]float64
}

func NewStaticService() *StaticService {
	return &StaticService{
		ratesToUSD: map[string]float64{
			"USD": 1,
			"IDR": 1.0 / 15800.0,
			"EUR": 1.08,
			"SGD": 0.74,
		},
	}
}

var _ Service = (*StaticService)(nil)

func (s *StaticService) GetExchangeRate(ctx context.Context, from, to string, at time.Time) (float64, error) {
	from = strings.ToUpper(from)
	to = strings.ToUpper(to)
	if from == to {
		return 1, nil
	}

	fromRate, ok := s.ratesToUSD[from]
	if !ok {
		return 0, fmt.Errorf("unsupported currency: %s", from)
	}
	toRate, ok := s.ratesToUSD[to]
	if !ok {
		return 0, fmt.Errorf("unsupported currency: %s", to)
	}

	return fromRate / toRate, nil
}
