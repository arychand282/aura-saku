package scheduler_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"aura-saku/internal/scheduler"
)

// TestRunner_TicksRepeatedlyUntilCanceled covers the core contract: a job
// fires more than once on its own interval, and stops firing once the
// Runner's context is canceled.
func TestRunner_TicksRepeatedlyUntilCanceled(t *testing.T) {
	var calls int32
	job := scheduler.Job{
		Name:     "test-job",
		Interval: 10 * time.Millisecond,
		Run: func(ctx context.Context) error {
			atomic.AddInt32(&calls, 1)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.New(job).Start(ctx)

	// Enough ticks at a 10ms interval to be confident it's actually
	// repeating, not just firing once.
	time.Sleep(55 * time.Millisecond)
	cancel()

	got := atomic.LoadInt32(&calls)
	if got < 3 {
		t.Fatalf("calls after ~55ms at a 10ms interval = %d, want at least 3", got)
	}

	// No further ticks after cancellation.
	afterCancel := atomic.LoadInt32(&calls)
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != afterCancel {
		t.Errorf("calls kept increasing after context cancellation: %d -> %d", afterCancel, got)
	}
}

// TestRunner_JobErrorDoesNotStopFutureTicks guards the swallow-and-continue
// contract: one tick returning an error must not stop that job (or any
// other) from ticking again.
func TestRunner_JobErrorDoesNotStopFutureTicks(t *testing.T) {
	var calls int32
	failingJob := scheduler.Job{
		Name:     "failing-job",
		Interval: 10 * time.Millisecond,
		Run: func(ctx context.Context) error {
			atomic.AddInt32(&calls, 1)
			return errors.New("boom")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	scheduler.New(failingJob).Start(ctx)

	time.Sleep(45 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("calls after ~45ms despite every tick erroring = %d, want at least 3 (errors must not stop future ticks)", got)
	}
}

// TestRunner_MultipleJobsRunIndependently covers two jobs with different
// intervals both firing, so one job's schedule/failure can never interfere
// with another's.
func TestRunner_MultipleJobsRunIndependently(t *testing.T) {
	var fastCalls, slowCalls int32
	fast := scheduler.Job{
		Name:     "fast",
		Interval: 5 * time.Millisecond,
		Run: func(ctx context.Context) error {
			atomic.AddInt32(&fastCalls, 1)
			return nil
		},
	}
	slow := scheduler.Job{
		Name:     "slow",
		Interval: 50 * time.Millisecond,
		Run: func(ctx context.Context) error {
			atomic.AddInt32(&slowCalls, 1)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	scheduler.New(fast, slow).Start(ctx)

	time.Sleep(60 * time.Millisecond)

	if got := atomic.LoadInt32(&fastCalls); got < 5 {
		t.Errorf("fast job calls = %d, want at least 5", got)
	}
	if got := atomic.LoadInt32(&slowCalls); got < 1 {
		t.Errorf("slow job calls = %d, want at least 1", got)
	}
}
