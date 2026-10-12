package mockqueue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// testClock is an injectable clock the tests advance by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Unix(1_700_000_000, 0)} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// script is a handler whose outcome per job type is set by the test.
type script struct {
	mu       sync.Mutex
	err      map[string]error
	received []string
}

func newScript() *script { return &script{err: map[string]error{}} }

func (s *script) set(jobType string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err[jobType] = err
}

func (s *script) handle(_ context.Context, job contract.QueueJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, job.Type)
	return s.err[job.Type]
}

func (s *script) count(jobType string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.received {
		if r == jobType {
			n++
		}
	}
	return n
}

func runConsume(t *testing.T, q *Fake, h func(context.Context, contract.QueueJob) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = q.Consume(ctx, nil, 1, h)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func newTiered(clock *testClock, opts ...Option) *Fake {
	return NewFake(append([]Option{WithClock(clock.Now)}, opts...)...)
}

func TestFake_JobFailureWaitsInTheFirstTierUntilItsDelayElapses(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock)
	h := newScript()
	h.set("a", errors.New("parse failure"))
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "a"})
	runConsume(t, q, h.handle)

	waitFor(t, func() bool { return len(q.Retries()) == 1 })
	r := q.Retries()[0]
	if r.Attempts != 1 || r.Tier != 0 || r.Delay != 5*time.Second || r.ProxyFault {
		t.Fatalf("retry = %+v, want attempts 1 in the 5s tier", r)
	}
	if n := len(q.Waiting(0)); n != 1 {
		t.Fatalf("tier 0 holds %d jobs, want 1", n)
	}

	clock.Advance(4 * time.Second)
	time.Sleep(20 * time.Millisecond)
	if h.count("a") != 1 {
		t.Fatalf("job redelivered before its delay elapsed (%d deliveries)", h.count("a"))
	}

	clock.Advance(time.Second)
	waitFor(t, func() bool { return h.count("a") == 2 })
}

func TestFake_RepeatedJobFailuresEscalateThroughTheTiers(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock)
	h := newScript()
	h.set("a", errors.New("parse failure"))
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "a"})
	runConsume(t, q, h.handle)

	wantTier := []int{0, 1, 2, 3, 3}
	delays := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, time.Hour, time.Hour}
	for i, tier := range wantTier {
		waitFor(t, func() bool { return len(q.Retries()) == i+1 })
		r := q.Retries()[i]
		if r.Tier != tier || r.Attempts != i+1 {
			t.Fatalf("failure %d: tier %d attempts %d, want tier %d attempts %d", i+1, r.Tier, r.Attempts, tier, i+1)
		}
		clock.Advance(delays[i])
	}
}

func TestFake_ProxyCausedFailureDoesNotCountAsAnAttempt(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock)
	h := newScript()
	h.set("a", &contract.ProxyCheckError{Kind: contract.CheckTarget, Challenge: true})
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "a"})
	runConsume(t, q, h.handle)

	for i := range 4 {
		waitFor(t, func() bool { return len(q.Retries()) == i+1 })
		r := q.Retries()[i]
		if r.Attempts != 0 || !r.ProxyFault || r.Tier != 0 {
			t.Fatalf("failure %d = %+v, want attempts 0 proxy fault in tier 0", i+1, r)
		}
		clock.Advance(5 * time.Second)
	}
	if len(q.Dead()) != 0 {
		t.Fatal("proxy-caused failures must never dead-park the job")
	}
}

func TestFake_ProxyFailureAfterJobFailureKeepsTheAttemptCount(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock)
	h := newScript()
	h.set("a", errors.New("parse failure"))
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "a"})
	runConsume(t, q, h.handle)

	waitFor(t, func() bool { return len(q.Retries()) == 1 })
	h.set("a", &contract.ProxyFaultError{Err: errors.New("no proxy")})
	clock.Advance(5 * time.Second)
	waitFor(t, func() bool { return len(q.Retries()) == 2 })
	if r := q.Retries()[1]; r.Attempts != 1 || !r.ProxyFault {
		t.Fatalf("retry = %+v, want the earlier attempt count kept", r)
	}
}

func TestFake_ExhaustedAttemptsGoToTheDeadQueue(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock, WithMaxAttempts(2))
	h := newScript()
	h.set("a", errors.New("parse failure"))
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "a"})
	runConsume(t, q, h.handle)

	waitFor(t, func() bool { return len(q.Retries()) == 1 })
	clock.Advance(5 * time.Second)
	waitFor(t, func() bool { return len(q.Dead()) == 1 })
	if n := len(q.Waiting(0)) + len(q.Waiting(1)); n != 0 {
		t.Fatalf("%d jobs still waiting after the attempt cap", n)
	}
}

func TestFake_PoisonAndMissingHandlerGoStraightToTheDeadQueue(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock)
	h := newScript()
	h.set("p", errors.Join(contract.ErrPoisonPayload, errors.New("bad json")))
	h.set("n", contract.ErrNoHandler)
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "p"})
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "n"})
	runConsume(t, q, h.handle)

	waitFor(t, func() bool { return len(q.Dead()) == 2 })
	if len(q.Retries()) != 0 {
		t.Fatalf("retries = %+v, want none", q.Retries())
	}
}

func TestFake_ALongDelayDoesNotHoldBackALaterShortOne(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock)
	h := newScript()
	h.set("slow", errors.New("fails twice"))
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "slow"})
	runConsume(t, q, h.handle)

	// slow fails twice and ends up in the 30s tier.
	waitFor(t, func() bool { return len(q.Retries()) == 1 })
	clock.Advance(5 * time.Second)
	waitFor(t, func() bool { return len(q.Retries()) == 2 })
	if r := q.Retries()[1]; r.Tier != 1 {
		t.Fatalf("slow job in tier %d, want 1", r.Tier)
	}

	// A later job fails once and waits in the 5s tier.
	h.set("fast", errors.New("fails once"))
	_ = q.Publish(context.Background(), contract.QueueJob{Type: "fast"})
	waitFor(t, func() bool { return len(q.Retries()) == 3 })
	h.set("fast", nil)

	clock.Advance(5 * time.Second)
	waitFor(t, func() bool { return h.count("fast") == 2 })
	if h.count("slow") != 2 || len(q.Waiting(1)) != 1 {
		t.Fatalf("slow job must still be waiting in tier 1 (deliveries %d)", h.count("slow"))
	}
}

func TestFake_LegacyFailedMessagesDrainWithoutSleeping(t *testing.T) {
	clock := newTestClock()
	q := newTiered(clock, WithMaxAttempts(10))
	h := newScript()
	q.PublishLegacyFailed(contract.QueueJob{Type: "due"}, 3, 0)
	q.PublishLegacyFailed(contract.QueueJob{Type: "waiting"}, 3, 20*time.Second)
	q.PublishLegacyFailed(contract.QueueJob{Type: "spent"}, 10, 0)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = q.ConsumeFailed(ctx, nil, 1)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitFor(t, func() bool { return len(q.Dead()) == 1 && len(q.Waiting(1)) == 1 })
	if q.Dead()[0].Type != "spent" || q.Waiting(1)[0].Type != "waiting" {
		t.Fatalf("dead %+v, tier 1 %+v", q.Dead(), q.Waiting(1))
	}

	runConsume(t, q, h.handle)
	waitFor(t, func() bool { return h.count("due") == 1 })
	if h.count("waiting") != 0 {
		t.Fatal("the waiting legacy message must honour its remaining backoff")
	}
	clock.Advance(30 * time.Second)
	waitFor(t, func() bool { return h.count("waiting") == 1 })
}

func TestFake_NewDefaultsMatchTheDocumentedLadder(t *testing.T) {
	q := NewFake()
	if got := q.tiers; len(got) != 4 || got[0] != 5*time.Second || got[3] != time.Hour {
		t.Fatalf("default tiers = %v", got)
	}
}
