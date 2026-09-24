package fx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// defaultFrankfurterBaseURL is used when baseURL isn't overridden via
// config (FX_PROVIDER_BASE_URL). Frankfurter has already changed domains
// once (api.frankfurter.app -> api.frankfurter.dev/v1), which is exactly
// why this is configurable rather than hardcoded only.
const defaultFrankfurterBaseURL = "https://api.frankfurter.dev/v1"

// LiveService fetches real exchange rates from a Frankfurter-compatible API
// (ECB reference rates, free, no API key) for the given date, falling back
// to a static rate table on any failure — a live-provider outage, domain
// change, or an unsupported currency pair should never block recording a
// transaction.
type LiveService struct {
	httpClient *http.Client
	baseURL    string
	fallback   Service
}

// NewLiveService builds a LiveService. An empty baseURL falls back to
// defaultFrankfurterBaseURL.
func NewLiveService(baseURL string, fallback Service) *LiveService {
	if baseURL == "" {
		baseURL = defaultFrankfurterBaseURL
	}
	return &LiveService{
		httpClient: &http.Client{Timeout: 8 * time.Second},
		baseURL:    baseURL,
		fallback:   fallback,
	}
}

var _ Service = (*LiveService)(nil)

func (s *LiveService) GetExchangeRate(ctx context.Context, from, to string, at time.Time) (float64, error) {
	from = strings.ToUpper(from)
	to = strings.ToUpper(to)
	if from == to {
		return 1, nil
	}

	rate, err := s.fetchRate(ctx, from, to, at)
	if err != nil {
		return s.fallback.GetExchangeRate(ctx, from, to, at)
	}
	return rate, nil
}

type frankfurterResponse struct {
	Rates map[string]float64 `json:"rates"`
}

func (s *LiveService) fetchRate(ctx context.Context, from, to string, at time.Time) (float64, error) {
	// Frankfurter only has historical data for past dates; a same-day (or
	// clock-skewed future) transaction should ask for "latest" rather than
	// a date the provider doesn't have published rates for yet.
	dateSegment := "latest"
	today := time.Now().UTC()
	todayMidnight := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if at.Before(todayMidnight) {
		dateSegment = at.Format("2006-01-02")
	}

	url := fmt.Sprintf("%s/%s?from=%s&to=%s", s.baseURL, dateSegment, from, to)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build frankfurter request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("frankfurter request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("frankfurter API error (status %d)", resp.StatusCode)
	}

	var body frankfurterResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode frankfurter response: %w", err)
	}

	rate, ok := body.Rates[to]
	if !ok {
		return 0, fmt.Errorf("frankfurter response missing rate for %s", to)
	}
	return rate, nil
}
