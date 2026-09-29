// Package scheduler runs a small, fixed set of named background jobs on
// their own interval — recurring transactions, bill reminders, and
// proactive budget nudges all need "do X every so often" with no inbound
// webhook to trigger them, and none of them need anything more precise than
// that yet. Deliberately just a goroutine-per-job around time.Ticker: this
// process runs as a single replica (see docker-compose.yml's "backend"
// service, no horizontal scaling config), so there's no double-firing risk
// to guard against with a distributed lock. If that ever changes, add a
// Postgres advisory lock or a Redis SET NX claim per tick then — not
// before, since neither is needed today.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"aura-saku/internal/logger"
)

// Job is one independently-scheduled unit of background work. Run fires
// once per Interval starting Interval after Runner.Start is called, not
// immediately — a job that must also run once at startup should be invoked
// directly before Start as well as being registered here.
type Job struct {
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error
}

// Runner drives a fixed set of Jobs, each on its own goroutine/ticker, until
// the context passed to Start is canceled.
type Runner struct {
	jobs []Job
}

// New builds a Runner over the given jobs. Not yet started — call Start.
func New(jobs ...Job) *Runner {
	return &Runner{jobs: jobs}
}

// Start launches every job's ticker goroutine and returns immediately
// (non-blocking); all of them stop once ctx is canceled.
func (r *Runner) Start(ctx context.Context) {
	for _, job := range r.jobs {
		go r.run(ctx, job)
	}
}

// run is one job's own ticker loop. A single tick's error is logged and
// swallowed rather than propagated — a transient failure (e.g. a DB blip)
// must never stop that job's future ticks or take down the process, since
// nothing else is watching this goroutine.
func (r *Runner) run(ctx context.Context, job Job) {
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := job.Run(ctx); err != nil {
				logger.Error(logger.SchedulerJobFailed, fmt.Sprintf("scheduled job %q", job.Name), err)
			}
		}
	}
}
