package mockqueue

import (
	"context"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Fake is an in-memory queue implementing contract.Queue for tests.
// Published jobs are buffered and delivered to Consume handlers. Like the
// real adapter, a job whose handler returns nil is acked (recorded in
// Acked) and one whose handler fails is retried (recorded in Retried), and
// Consume does not return until in-flight handlers have finished.
type Fake struct {
	mu      sync.Mutex
	jobs    []contract.QueueJob
	acked   []contract.QueueJob
	retried []contract.QueueJob
}

// NewFake creates a new Fake queue.
func NewFake() *Fake {
	return &Fake{}
}

// Publish appends a job to the in-memory buffer.
func (f *Fake) Publish(_ context.Context, job contract.QueueJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return nil
}

// Acked returns the jobs whose handler succeeded, in ack order.
func (f *Fake) Acked() []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.QueueJob(nil), f.acked...)
}

// Retried returns the jobs whose handler returned an error. The fake does not
// redeliver them.
func (f *Fake) Retried() []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.QueueJob(nil), f.retried...)
}

// Consume runs concurrency consumers (at least one) that drain buffered jobs
// and call handler for each. Jobs published after Consume starts are also
// delivered. It blocks until ctx is cancelled and every in-flight handler
// has returned; handlers receive a context that ctx's cancellation does not
// cancel.
func (f *Fake) Consume(ctx context.Context, _ []string, concurrency int, handler func(ctx context.Context, job contract.QueueJob) error) error {
	if concurrency < 1 {
		concurrency = 1
	}
	// Like the real adapter, cancelling ctx only stops new claims: in-flight
	// handlers run on a context that outlives it.
	processCtx, processCancel := context.WithCancel(context.Background())
	defer processCancel()
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				f.mu.Lock()
				if len(f.jobs) > 0 {
					job := f.jobs[0]
					f.jobs = f.jobs[1:]
					f.mu.Unlock()
					err := handler(processCtx, job)
					f.mu.Lock()
					if err == nil {
						f.acked = append(f.acked, job)
					} else {
						f.retried = append(f.retried, job)
					}
					f.mu.Unlock()
					continue
				}
				f.mu.Unlock()

				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Millisecond):
				}
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}

// Close is a no-op.
func (f *Fake) Close() error { return nil }

// ConsumeFailed is a no-op for the mock queue.
func (f *Fake) ConsumeFailed(_ context.Context, _ []string, _ int) error { return nil }
