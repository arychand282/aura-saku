package fx

import (
	"context"
	"fmt"
	"strings"
)

// Service resolves an exchange rate between two ISO 4217 currency codes.
type Service interface {
	GetExchangeRate(ctx context.Context, from, to string) (float64, error)
}

// StaticService is a fixed dummy-rate implementation so transaction
// persistence has something real to call during scaffold/dev.
//
// TODO: replace with a real historical FX rate provider (e.g.
// exchangerate.host or a paid provider) that returns the rate as of a given
// transaction timestamp, not just the current rate.
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

func (s *StaticService) GetExchangeRate(ctx context.Context, from, to string) (float64, error) {
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
