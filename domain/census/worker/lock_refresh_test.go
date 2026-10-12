package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	censushandler "github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// fakeClock is a manually advanced clock shared by the worker and the fake
// proxy repository, so lock and freshness windows move together.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// refreshCountingRepo counts RefreshConsumerLock calls, the database write
// the throttle exists to avoid.
type refreshCountingRepo struct {
	*repository.FakeProxyRepository
	refreshes atomic.Int32
}

func (r *refreshCountingRepo) RefreshConsumerLock(ctx context.Context, id int64, owner string, ttl time.Duration) (*contract.ProxyRecord, error) {
	r.refreshes.Add(1)
	return r.FakeProxyRepository.RefreshConsumerLock(ctx, id, owner, ttl)
}

// continuingQueue hands out the published jobs in order and keeps consuming
// after a failed job, the way a real queue nacks one delivery and moves on.
// It cancels the consumer once every job has been handled.
type continuingQueue struct {
	jobs   []contract.QueueJob
	cancel context.CancelFunc
}

func (q *continuingQueue) Publish(context.Context, contract.QueueJob) error { return nil }
func (q *continuingQueue) ConsumeFailed(context.Context, []string, int) error {
	return nil
}
func (q *continuingQueue) Close() error { return nil }

func (q *continuingQueue) Consume(ctx context.Context, _ []string, _ int, fn func(context.Context, contract.QueueJob) error) error {
	for _, job := range q.jobs {
		_ = fn(ctx, job)
	}
	q.cancel()
	<-ctx.Done()
	return ctx.Err()
}

// addrHandler runs script with the 1-based call number and the address of the
// proxy the handler was built for.
type addrHandler struct {
	addr   string
	calls  *atomic.Int32
	script func(call int, addr string)
	errFor func(call int) error
}

func (h *addrHandler) Handle(context.Context, []byte) ([]contract.QueueJob, error) {
	call := int(h.calls.Add(1))
	h.script(call, h.addr)
	if h.errFor != nil {
		return nil, h.errFor(call)
	}
	return nil, nil
}

type lockRefreshRun struct {
	repo  *refreshCountingRepo
	clock *fakeClock
	hub   *proxydomain.ProxyHub
	// addrs[i] is the proxy address that handled call i+1.
	mu    sync.Mutex
	addrs []string
}

// newLockRefreshRun builds a hub over two proxies. Proxy 1 is fresh and is
// claimed first; proxy 2 is fresh too unless spareFresh is false. Evidence
// stays fresh for freshnessTTL on the shared fake clock.
func newLockRefreshRun(t *testing.T, freshnessTTL time.Duration, spareFresh bool) *lockRefreshRun {
	t.Helper()
	clock := newFakeClock()
	policy := workerPolicy
	policy.FreshnessTTL = freshnessTTL
	base := repository.NewFakeProxyRepository(policy)
	base.Now = clock.Now
	high, low := 90.0, 10.0
	for _, rec := range []contract.ProxyRecord{
		{Protocol: "http", IP: "1.1.1.1", Port: 8080, Source: "test", UptimePercent: &high},
		{Protocol: "http", IP: "2.2.2.2", Port: 8080, Source: "test", UptimePercent: &low},
	} {
		if _, _, err := base.InsertIfAbsent(context.Background(), rec); err != nil {
			t.Fatalf("InsertIfAbsent: %v", err)
		}
	}
	if !base.SeedSuccess(1, 100) || (spareFresh && !base.SeedSuccess(2, 100)) {
		t.Fatal("failed to seed proxy evidence")
	}
	repo := &refreshCountingRepo{FakeProxyRepository: base}
	return &lockRefreshRun{
		repo:  repo,
		clock: clock,
		hub:   proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, policy),
	}
}

// run feeds jobs to a one-goroutine proxy worker. before(call, addr) runs
// inside the handler as the call starts and may advance the clock or change
// proxy state; errFor decides the call's result.
func (r *lockRefreshRun) run(t *testing.T, jobs int, before func(call int, addr string), errFor func(call int) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q := &continuingQueue{cancel: cancel}
	for range jobs {
		q.jobs = append(q.jobs, contract.QueueJob{Type: censushandler.EventIDSweep, Payload: []byte(`{}`)})
	}
	w := New(q, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w.now = r.clock.Now

	var calls atomic.Int32
	newHandlers := func(client contract.LodestoneClient, _ contract.ProviderRateLimiter) *censushandler.Registry {
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, &addrHandler{
			addr:  client.(*addrClient).addr,
			calls: &calls,
			script: func(call int, addr string) {
				r.mu.Lock()
				r.addrs = append(r.addrs, addr)
				r.mu.Unlock()
				if before != nil {
					before(call, addr)
				}
			},
			errFor: errFor,
		})
		return reg
	}
	newClient := func(addr string, _ contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
		return &addrClient{addr: addr}, nil
	}
	err := w.RunEventsWithProxy(ctx, []string{censushandler.EventIDSweep}, 1, "test", r.hub,
		newHandlers, newClient, func() contract.ProviderRateLimiter { return nil })
	if err != nil && ctx.Err() == nil {
		t.Fatalf("RunEventsWithProxy: %v", err)
	}
	if got := int(calls.Load()); got != jobs {
		t.Fatalf("expected %d handler calls, got %d", jobs, got)
	}
}

type addrClient struct {
	fakeLodestoneClient
	addr     string
	requests *atomic.Int64 // counts every request that reaches the Lodestone through this client; may be nil
}

func (c *addrClient) FetchCharacter(ctx context.Context, id uint32) (*contract.CharacterProfile, error) {
	if c.requests != nil {
		c.requests.Add(1)
	}
	return c.fakeLodestoneClient.FetchCharacter(ctx, id)
}

func (r *lockRefreshRun) addrOf(call int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.addrs[call-1]
}

func TestProxyWorkerLockRefresh_JobsWithinTheIntervalRefreshOnce(t *testing.T) {
	run := newLockRefreshRun(t, time.Hour, true)
	run.run(t, 5, func(int, string) { run.clock.Advance(10 * time.Second) }, nil)

	if got := run.repo.refreshes.Load(); got != 1 {
		t.Fatalf("expected one lock refresh for five jobs within the interval, got %d", got)
	}
}

func TestProxyWorkerLockRefresh_RefreshesAgainOnceTheIntervalHasPassed(t *testing.T) {
	run := newLockRefreshRun(t, time.Hour, true)
	// Job starts at 0s, 40s, 80s, 120s, 160s: the refresh at 0s is due again
	// at 120s (>= 100s) and then not before 220s.
	run.run(t, 5, func(int, string) { run.clock.Advance(40 * time.Second) }, nil)

	if got := run.repo.refreshes.Load(); got != 2 {
		t.Fatalf("expected a second refresh after the interval, got %d refreshes", got)
	}
	for call := 1; call <= 5; call++ {
		if run.addrOf(call) != "http://1.1.1.1:8080" {
			t.Fatalf("call %d moved to %s, expected the held proxy to be kept", call, run.addrOf(call))
		}
	}
}

func TestProxyWorkerLockRefresh_ProxyThatLostFreshnessIsDroppedWithinTheBound(t *testing.T) {
	run := newLockRefreshRun(t, 2*time.Minute, false)
	run.run(t, 3, func(call int, _ string) {
		switch call {
		case 1:
			// Freshness (2m) lapses while the proxy is held, and the scanner
			// brings the spare proxy up to date.
			run.clock.Advance(130 * time.Second)
			if !run.repo.SeedSuccess(2, 100) {
				t.Error("failed to re-seed the spare proxy")
			}
		case 2:
			run.clock.Advance(time.Second)
		}
	}, nil)

	if first, second := run.addrOf(1), run.addrOf(2); first != "http://1.1.1.1:8080" || second != "http://2.2.2.2:8080" {
		t.Fatalf("expected the stale proxy replaced by the spare at the first due refresh, got %s then %s", first, second)
	}
	if third := run.addrOf(3); third != "http://2.2.2.2:8080" {
		t.Fatalf("expected the replacement kept, got %s", third)
	}
	stale, err := run.repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stale.LockedBy != nil {
		t.Errorf("expected the dropped proxy's lock released, still held by %s", *stale.LockedBy)
	}
}

func TestProxyWorkerLockRefresh_ProxyInCooldownIsDroppedWithinTheBound(t *testing.T) {
	run := newLockRefreshRun(t, time.Hour, true)
	run.run(t, 3, func(call int, _ string) {
		switch call {
		case 1:
			// Another process cools the destination for this proxy.
			if err := run.repo.CooldownDestination(context.Background(), 1, "test-w0", 5*time.Minute, 10*time.Minute); err != nil {
				t.Errorf("CooldownDestination: %v", err)
			}
			run.clock.Advance(60 * time.Second)
		case 2:
			run.clock.Advance(60 * time.Second)
		}
	}, nil)

	// 60s after the refresh the throttle still trusts the proxy; at 120s the
	// refresh is due and rejects it.
	if got := []string{run.addrOf(1), run.addrOf(2), run.addrOf(3)}; got[0] != "http://1.1.1.1:8080" || got[1] != "http://1.1.1.1:8080" || got[2] != "http://2.2.2.2:8080" {
		t.Fatalf("expected the cooled proxy kept until the refresh is due and then replaced, got %v", got)
	}
}

func TestProxyWorkerLockRefresh_ProxyCausedFailureForcesRevalidation(t *testing.T) {
	cases := []struct {
		name          string
		failure       error
		wantRefreshes int32
		wantThirdAddr string
	}{
		// The destination cooldown releases the lock, so the next job must
		// notice at once instead of running on a proxy it no longer holds.
		// Refreshes: the first hand-out, the check that finds the lock gone,
		// and the replacement's hand-out.
		{"destination rejection", typedJobErr(contract.CheckTarget, time.Minute), 3, "http://2.2.2.2:8080"},
		// A timeout no longer leaves the proxy in place: the job is retried
		// in-process on another proxy (see proxy_retry_test.go).
		// A job error that says nothing about the proxy does not cost a write.
		{"business error", errors.New("character page layout changed"), 1, "http://1.1.1.1:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := newLockRefreshRun(t, time.Hour, true)
			run.run(t, 3, func(int, string) { run.clock.Advance(5 * time.Second) },
				func(call int) error {
					if call == 2 {
						return tc.failure
					}
					return nil
				})

			if got := run.repo.refreshes.Load(); got != tc.wantRefreshes {
				t.Errorf("expected %d lock refreshes, got %d", tc.wantRefreshes, got)
			}
			if got := run.addrOf(3); got != tc.wantThirdAddr {
				t.Errorf("expected the job after the failure on %s, got %s", tc.wantThirdAddr, got)
			}
		})
	}
}
