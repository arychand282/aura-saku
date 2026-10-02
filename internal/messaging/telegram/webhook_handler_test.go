package telegram

import (
	"sync"
	"testing"
)

// TestWebhookWorkerPool_ProcessesSubmittedJobs covers the basic case: every
// submitted job actually runs, across multiple workers.
func TestWebhookWorkerPool_ProcessesSubmittedJobs(t *testing.T) {
	pool := newWebhookWorkerPool(2, 10)

	var mu sync.Mutex
	count := 0
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		ok := pool.submit(func() {
			defer wg.Done()
			mu.Lock()
			count++
			mu.Unlock()
		})
		if !ok {
			t.Fatalf("submit %d was rejected, want accepted (pool has room)", i)
		}
	}
	wg.Wait()

	if count != 5 {
		t.Errorf("count = %d, want 5 (every submitted job should have run)", count)
	}
}

// TestWebhookWorkerPool_RejectsWhenFull covers the backpressure guarantee
// this pool exists for: once every worker is busy and the queue itself is
// full, submit must return false immediately (so the webhook handler can
// respond 503 and let Telegram retry) rather than block the caller.
func TestWebhookWorkerPool_RejectsWhenFull(t *testing.T) {
	pool := newWebhookWorkerPool(1, 1)

	started := make(chan struct{})
	release := make(chan struct{})

	// Occupy the single worker with a job that blocks until released.
	if !pool.submit(func() {
		close(started)
		<-release
	}) {
		t.Fatal("expected the first submit to be accepted")
	}
	<-started // the worker has now picked up the job, leaving it fully busy

	// Fill the one-slot queue with a second job.
	if !pool.submit(func() {}) {
		t.Fatal("expected the second submit to be accepted (fills the queue)")
	}

	// Worker busy AND queue full -> this one must be rejected.
	if pool.submit(func() {}) {
		t.Error("expected the third submit to be rejected (pool full), was accepted")
	}

	close(release) // let the pool drain so the goroutine doesn't leak past the test
}
