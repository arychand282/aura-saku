package budget

import (
	"context"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
)

// Status is the shape returned by EvaluateBudget — the post-entry feedback
// shown to the user and surfaced on the dashboard.
type Status struct {
	NeedsSpent, NeedsLimit     float64
	WantsSpent, WantsLimit     float64
	SavingsSpent, SavingsLimit float64
	Warnings                   []string
}

// Service owns the 50/30/20 budget guardrail logic: post-entry feedback,
// anomaly/impulse detection, and end-of-month rollover suggestions. Method
// bodies are TODO-stubbed for this scaffold pass.
type Service struct {
	db *gorm.DB
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// EvaluateBudget computes the user's current period spend against their
// configured Needs/Wants/Savings allocation.
//
// TODO: load the user's active Budget row, sum ConvertedAmount of this
// period's Transactions grouped by Category.Bucket, compare against the
// allocation percentages of the user's period income, and populate Warnings
// for anomaly/impulse spikes (e.g. Wants bucket spend rate far exceeding a
// linear pace through the month).
func (s *Service) EvaluateBudget(ctx context.Context, userID uint) (*Status, error) {
	return nil, apperror.ErrNotImplemented
}

// SuggestRollover proposes shifting an end-of-month surplus into savings.
//
// TODO: compute (period income - period spend) for the user's most recently
// completed period and, if positive, return a suggested savings rollover
// message.
func (s *Service) SuggestRollover(ctx context.Context, userID uint) (string, error) {
	return "", apperror.ErrNotImplemented
}
