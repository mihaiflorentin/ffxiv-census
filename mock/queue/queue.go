package mockqueue

import (
	"context"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Retry records one failed delivery and where the fake put it.
type Retry struct {
	Job contract.QueueJob
	// Attempts is the job-attributable failure count the retried message
	// carries; ProxyFault marks a failure that left it unchanged.
	Attempts   int
	ProxyFault bool
	Tier       int
	Delay      time.Duration
}

// Option configures a Fake.
type Option func(*Fake)

// WithClock injects the clock the delay tiers use, so tests advance time by
// hand instead of sleeping.
func WithClock(now func() time.Time) Option { return func(f *Fake) { f.now = now } }

// WithMaxAttempts overrides the attempt cap that dead-parks a job (default 50).
func WithMaxAttempts(n int) Option { return func(f *Fake) { f.maxAttempts = n } }

// WithProxyFailureCap overrides how many proxy-attributable failures a job
// absorbs before further ones count as attempts (default
// contract.DefaultProxyFailureCap).
func WithProxyFailureCap(n int) Option { return func(f *Fake) { f.proxyFailureCap = n } }

// entry is a message inside the fake with the state the broker keeps in
// headers.
type entry struct {
	job      contract.QueueJob
	attempts int
	// proxyFailures is the x-proxy-failures header of the real adapter.
	proxyFailures int
	readyAt       time.Time
}

// Fake is an in-memory queue implementing contract.Queue for tests. It models
// the real adapter's delay tiers: a job whose handler fails waits in the tier
// chosen by contract.DecideRetry and becomes deliverable again once the tier
// delay has passed on the injected clock; poison and exhausted jobs land in
// the dead list. A job whose handler returns nil is acked (recorded in
// Acked) and every failure is recorded in Retried and Retries. Consume does
// not return until in-flight handlers have finished.
type Fake struct {
	mu              sync.Mutex
	now             func() time.Time
	tiers           []time.Duration
	maxAttempts     int
	proxyFailureCap int
	jobs            []entry
	delayed         [][]entry // per tier, in arrival order
	legacy          []entry
	dead            []contract.QueueJob
	acked           []contract.QueueJob
	retried         []contract.QueueJob
	retries         []Retry
}

// NewFake creates a new Fake queue.
func NewFake(opts ...Option) *Fake {
	f := &Fake{now: time.Now, tiers: contract.DefaultRetryTiers(), maxAttempts: 50, proxyFailureCap: contract.DefaultProxyFailureCap}
	for _, opt := range opts {
		opt(f)
	}
	f.delayed = make([][]entry, len(f.tiers))
	return f
}

// Publish appends a job to the in-memory buffer.
func (f *Fake) Publish(_ context.Context, job contract.QueueJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, entry{job: job})
	return nil
}

// PublishLegacyFailed places a job in the pre-tier failed queue, carrying
// attempts and still owing remaining backoff, for ConsumeFailed to drain.
func (f *Fake) PublishLegacyFailed(job contract.QueueJob, attempts int, remaining time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.legacy = append(f.legacy, entry{job: job, attempts: attempts, readyAt: f.now().Add(remaining)})
}

// Acked returns the jobs whose handler succeeded, in ack order.
func (f *Fake) Acked() []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.QueueJob(nil), f.acked...)
}

// Retried returns the jobs whose handler returned an error, one element per
// failure (poison and exhausted jobs included).
func (f *Fake) Retried() []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.QueueJob(nil), f.retried...)
}

// Retries returns, per retried failure, the tier and attempt count chosen.
func (f *Fake) Retries() []Retry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Retry(nil), f.retries...)
}

// Waiting returns the jobs sitting in the given delay tier.
func (f *Fake) Waiting(tier int) []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	if tier < 0 || tier >= len(f.delayed) {
		return nil
	}
	out := make([]contract.QueueJob, 0, len(f.delayed[tier]))
	for _, e := range f.delayed[tier] {
		out = append(out, e.job)
	}
	return out
}

// Dead returns the jobs parked on the dead queue.
func (f *Fake) Dead() []contract.QueueJob {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]contract.QueueJob(nil), f.dead...)
}

// next removes and returns the next deliverable message: main queue first,
// then any delay-tier message whose delay has elapsed. A waiting message
// never blocks one behind it. Callers hold f.mu.
func (f *Fake) next() (entry, bool) {
	if len(f.jobs) > 0 {
		e := f.jobs[0]
		f.jobs = f.jobs[1:]
		return e, true
	}
	now := f.now()
	for tier := range f.delayed {
		for i, e := range f.delayed[tier] {
			if !e.readyAt.After(now) {
				f.delayed[tier] = append(f.delayed[tier][:i:i], f.delayed[tier][i+1:]...)
				return e, true
			}
		}
	}
	return entry{}, false
}

// fail applies the same decision as the broker adapter. Callers hold f.mu.
func (f *Fake) fail(e entry, err error) {
	f.retried = append(f.retried, e.job)
	d := contract.DecideRetry(err, e.attempts, e.proxyFailures, f.maxAttempts, f.proxyFailureCap, f.tiers)
	if d.Dead {
		f.dead = append(f.dead, e.job)
		return
	}
	f.retries = append(f.retries, Retry{Job: e.job, Attempts: d.Attempts, ProxyFault: d.ProxyFault, Tier: d.Tier, Delay: d.Delay})
	proxyFailures := e.proxyFailures
	if d.ProxyFault {
		proxyFailures++
	}
	f.delayed[d.Tier] = append(f.delayed[d.Tier], entry{job: e.job, attempts: d.Attempts, proxyFailures: proxyFailures, readyAt: f.now().Add(d.Delay)})
}

// Consume runs concurrency consumers (at least one) that drain buffered jobs
// and call handler for each. Jobs published after Consume starts are also
// delivered, as are delayed jobs once their tier delay has elapsed. It blocks
// until ctx is cancelled and every in-flight handler has returned; handlers
// receive a context that ctx's cancellation does not cancel.
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
				if e, ok := f.next(); ok {
					f.mu.Unlock()
					err := handler(processCtx, e.job)
					f.mu.Lock()
					if err == nil {
						f.acked = append(f.acked, e.job)
					} else {
						f.fail(e, err)
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

// ConsumeFailed drains the legacy failed queue like the real retry worker:
// without sleeping, it moves each message to the main queue, into the delay
// tier covering its remaining wait, or to the dead list. It blocks until ctx
// is cancelled.
func (f *Fake) ConsumeFailed(ctx context.Context, _ []string, _ int) error {
	for {
		f.mu.Lock()
		legacy := f.legacy
		f.legacy = nil
		for _, e := range legacy {
			d := contract.DecideLegacyDrain(e.attempts, f.maxAttempts, e.readyAt.Sub(f.now()), f.tiers)
			switch {
			case d.Dead:
				f.dead = append(f.dead, e.job)
			case d.Direct:
				f.jobs = append(f.jobs, entry{job: e.job, attempts: e.attempts})
			default:
				f.delayed[d.Tier] = append(f.delayed[d.Tier], entry{job: e.job, attempts: e.attempts, readyAt: f.now().Add(d.Delay)})
			}
		}
		f.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Millisecond):
		}
	}
}
