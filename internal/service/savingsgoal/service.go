// Package savingsgoal owns creation, cancellation, and progress tracking
// for SavingsGoal rows — a user-defined savings target ("save 10 million
// for a laptop by December") tracked against real savings activity rather
// than manually-logged contributions. Only one goal is active per user at
// a time (see models.SavingsGoal's doc comment); this package depends on
// budget.Service for the shared "what counts as savings" definition
// (budget.Service.SavingsSince) rather than computing its own, possibly
// inconsistent, aggregation.
package savingsgoal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"aura-saku/internal/apperror"
	"aura-saku/internal/models"
	"aura-saku/internal/service/budget"
)

type Service struct {
	db        *gorm.DB
	budgetSvc *budget.Service
}

func NewService(db *gorm.DB, budgetSvc *budget.Service) *Service {
	return &Service{db: db, budgetSvc: budgetSvc}
}

// Progress bundles a goal with how much has been saved toward it so far.
type Progress struct {
	Goal  *models.SavingsGoal
	Saved float64
}

// Create replaces the user's active goal (if any) with a new one — only
// one goal is active at a time — deactivating the previous rather than
// deleting it, the same preserve-history pattern
// budget.Service.SetBudget uses for allocation changes. The replaced return
// value is the goal that was just deactivated (nil if there wasn't one),
// so the caller (bot.Service.handleSavingsGoalCreate) can tell the user
// their previous goal was replaced instead of silently losing track of it —
// the reply used to say nothing at all about it.
func (s *Service) Create(ctx context.Context, userID uint, name string, targetAmount float64, currency string, deadline *time.Time) (newGoal *models.SavingsGoal, replaced *models.SavingsGoal, err error) {
	name = strings.TrimSpace(name)
	if name == "" || targetAmount <= 0 {
		return nil, nil, apperror.ErrInvalidSavingsGoal
	}

	goal := &models.SavingsGoal{
		UserID:       userID,
		Name:         name,
		TargetAmount: targetAmount,
		Currency:     strings.ToUpper(currency),
		Deadline:     deadline,
		IsActive:     true,
	}

	err = s.db.WithContext(ctx).Transaction(func(txDB *gorm.DB) error {
		var existing models.SavingsGoal
		lookupErr := txDB.Where("user_id = ? AND is_active = ?", userID, true).First(&existing).Error
		switch {
		case lookupErr == nil:
			replaced = &existing
		case errors.Is(lookupErr, gorm.ErrRecordNotFound):
			// No previous active goal — replaced stays nil, nothing to report.
		default:
			return fmt.Errorf("look up previous active goal: %w", lookupErr)
		}

		if err := txDB.Model(&models.SavingsGoal{}).
			Where("user_id = ? AND is_active = ?", userID, true).
			Update("is_active", false).Error; err != nil {
			return fmt.Errorf("deactivate previous goal: %w", err)
		}
		if err := txDB.Create(goal).Error; err != nil {
			return fmt.Errorf("insert new goal: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return goal, replaced, nil
}

// Active returns the user's current active goal, or
// apperror.ErrNoActiveSavingsGoal if there isn't one.
func (s *Service) Active(ctx context.Context, userID uint) (*models.SavingsGoal, error) {
	var goal models.SavingsGoal
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ?", userID, true).
		First(&goal).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.ErrNoActiveSavingsGoal
		}
		return nil, fmt.Errorf("find active goal: %w", err)
	}
	return &goal, nil
}

// Progress computes how much the user has saved toward their active goal
// since it was created, via budget.Service.SavingsSince. Returns
// apperror.ErrNoActiveSavingsGoal if there's no active goal.
func (s *Service) Progress(ctx context.Context, userID uint) (*Progress, error) {
	goal, err := s.Active(ctx, userID)
	if err != nil {
		return nil, err
	}
	saved, err := s.budgetSvc.SavingsSince(ctx, userID, goal.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("compute savings progress: %w", err)
	}
	return &Progress{Goal: goal, Saved: saved}, nil
}

// Cancel deactivates the user's active goal (not soft-deletes — history
// stays queryable, same convention as recurring.Service.Cancel). Returns
// apperror.ErrNoActiveSavingsGoal if there isn't one.
func (s *Service) Cancel(ctx context.Context, userID uint) (*models.SavingsGoal, error) {
	goal, err := s.Active(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(goal).Update("is_active", false).Error; err != nil {
		return nil, fmt.Errorf("deactivate goal %d: %w", goal.ID, err)
	}
	goal.IsActive = false
	return goal, nil
}
