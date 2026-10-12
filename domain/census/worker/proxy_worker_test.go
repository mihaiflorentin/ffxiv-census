package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	censushandler "github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// fakeProxyQueue is a minimal queue for proxy rotation tests.
type fakeProxyQueue struct {
	mu   sync.Mutex
	jobs []contract.QueueJob
}

func (q *fakeProxyQueue) Publish(_ context.Context, job contract.QueueJob) error {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.mu.Unlock()
	return nil
}

func (q *fakeProxyQueue) Consume(ctx context.Context, _ []string, _ int, fn func(context.Context, contract.QueueJob) error) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		q.mu.Lock()
		if len(q.jobs) == 0 {
			q.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		job := q.jobs[0]
		q.jobs = q.jobs[1:]
		q.mu.Unlock()

		if err := fn(ctx, job); err != nil {
			return err
		}
	}
}

func (q *fakeProxyQueue) ConsumeFailed(context.Context, []string, int) error { return nil }
func (q *fakeProxyQueue) Close() error                                       { return nil }

// noopCensusHandler handles census events without doing real work.
type noopCensusHandler struct{}

func (h *noopCensusHandler) Handle(_ context.Context, _ []byte) ([]contract.QueueJob, error) {
	return nil, nil
}

// countingCensusHandler counts invocations.
type countingCensusHandler struct {
	count *int32
}

func (h *countingCensusHandler) Handle(_ context.Context, _ []byte) ([]contract.QueueJob, error) {
	atomic.AddInt32(h.count, 1)
	return nil, nil
}

// fakeLodestoneClient implements contract.LodestoneClient for tests.
type fakeLodestoneClient struct{}

func (c *fakeLodestoneClient) FetchCharacter(context.Context, uint32) (*contract.CharacterProfile, error) {
	return nil, nil
}

func (c *fakeLodestoneClient) FetchAchievements(context.Context, uint32, []uint32) (*contract.AchievementSummary, error) {
	return nil, nil
}

func newTestCensusWorker(q contract.Queue, logger contract.Logger) *Worker {
	return New(q, nil, logger)
}

func newTestHandlers() func(contract.LodestoneClient, contract.ProviderRateLimiter) *censushandler.Registry {
	return func(_ contract.LodestoneClient, _ contract.ProviderRateLimiter) *censushandler.Registry {
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, &noopCensusHandler{})
		return reg
	}
}

func TestProxyWorkerLoop_WaitsForProxy(t *testing.T) {
	// Start with no proxies. Worker should wait, then proceed when one appears.
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}

	q.Publish(context.Background(), contract.QueueJob{
		Type:    censushandler.EventIDSweep,
		Payload: []byte(`{}`),
	})

	w := newTestCensusWorker(q, logger)
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	var started int32
	handlers := func(_ contract.LodestoneClient, _ contract.ProviderRateLimiter) *censushandler.Registry {
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, &countingCensusHandler{count: &started})
		return reg
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- w.RunEventsWithProxy(
			ctx,
			[]string{censushandler.EventIDSweep},
			1,
			"test",
			proxyHub,
			handlers,
			func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
				return &fakeLodestoneClient{}, nil
			},
			func() contract.ProviderRateLimiter { return nil },
		)
	}()

	// Worker should be waiting — no proxy yet.
	time.Sleep(200 * time.Millisecond)
	if atomic.LoadInt32(&started) > 0 {
		t.Fatal("worker started before proxy was available")
	}

	// Insert a proxy and seed fresh evidence so the hub hands it out.
	id, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "test",
	})
	if err != nil {
		t.Fatalf("InsertIfAbsent: %v", err)
	}
	if !repo.SeedSuccess(id, 100) {
		t.Fatal("failed to seed proxy evidence")
	}

	// Wait for the worker to pick it up.
	deadline := time.After(5 * time.Second)
	for atomic.LoadInt32(&started) == 0 {
		select {
		case <-deadline:
			t.Fatal("worker did not start after proxy became available")
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	<-done
}

func TestReplaceProxy_ReleasesBadBeforeReplacement(t *testing.T) {
	// A one-row pool: the replacement can only be claimed because the
	// previous claim is released first — hold-and-wait acquisition would
	// spin forever instead of rotating.
	repo := repository.NewFakeProxyRepository(workerPolicy)
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.1.1.1", Port: 8080, Source: "test",
	})
	if !repo.SeedSuccess(1, 100) {
		t.Fatal("failed to seed proxy evidence")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	w := New(q, nil, logger)
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, workerPolicy)

	owner := "test-w0"
	p1, err := proxyHub.NewProxy(context.Background(), owner)
	if err != nil || p1 == nil {
		t.Fatalf("NewProxy: proxy=%v err=%v", p1, err)
	}

	p2, _, _, _, err := w.replaceProxy(
		context.Background(),
		p1,
		nil,
		owner,
		proxyHub,
		func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
			return &fakeLodestoneClient{}, nil
		},
		func() contract.ProviderRateLimiter { return nil },
		newTestHandlers(),
	)
	if err != nil {
		t.Fatalf("replaceProxy: %v", err)
	}
	if p2 == nil {
		t.Fatal("expected replacement proxy")
	}

	// The replacement reclaims the lone released row: its evidence must be
	// untouched because the consumer gives up the claim without recording a
	// failure.
	rec, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if rec.FailCount != 0 || !rec.GeneralHealthy {
		t.Errorf("expected general evidence untouched, got fail_count=%d healthy=%v", rec.FailCount, rec.GeneralHealthy)
	}
	if rec.LockedBy == nil || *rec.LockedBy != owner {
		t.Errorf("expected the replacement to hold the claim for %s, got %v", owner, rec.LockedBy)
	}
}

func TestReplaceProxy_ReplacementTransportError(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	// Seed distinct uptimes so ClaimProxy order is deterministic: p2 is the
	// better proxy and is handed out first.
	lowUptime, highUptime := 10.0, 90.0
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.1.1.1", Port: 8080, Source: "test", UptimePercent: &lowUptime,
	})
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "2.2.2.2", Port: 8080, Source: "test", UptimePercent: &highUptime,
	})
	if !repo.SeedSuccess(1, 100) || !repo.SeedSuccess(2, 100) {
		t.Fatal("failed to seed proxy evidence")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	w := New(q, nil, logger)
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	owner := "test-w0"
	p1, err := proxyHub.NewProxy(context.Background(), owner)
	if err != nil || p1 == nil {
		t.Fatalf("NewProxy: proxy=%v err=%v", p1, err)
	}

	p2, _, _, _, err := w.replaceProxy(
		context.Background(),
		p1,
		nil,
		owner,
		proxyHub,
		func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
			return &fakeLodestoneClient{}, nil
		},
		func() contract.ProviderRateLimiter { return nil },
		newTestHandlers(),
	)
	if err != nil {
		t.Fatalf("replaceProxy: %v", err)
	}

	// Give up the replacement claim (simulating transport error handling):
	// the claim is released and general evidence stays untouched.
	if p2 != nil {
		if err := p2.Release(context.Background(), owner); err != nil {
			t.Fatalf("Release: %v", err)
		}
		rec, _ := repo.Get(context.Background(), p2.ID())
		if rec.LockedBy != nil {
			t.Errorf("expected replacement proxy released, still locked by %s", *rec.LockedBy)
		}
		if rec.FailCount != 0 || !rec.GeneralHealthy {
			t.Errorf("expected general evidence untouched, got fail_count=%d healthy=%v", rec.FailCount, rec.GeneralHealthy)
		}
	}
}

func TestProxyWorkerLoop_CancellationWhileWaiting(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	w := newTestCensusWorker(q, logger)
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := w.RunEventsWithProxy(
		ctx,
		[]string{censushandler.EventIDSweep},
		1,
		"test",
		proxyHub,
		newTestHandlers(),
		func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
			return &fakeLodestoneClient{}, nil
		},
		func() contract.ProviderRateLimiter { return nil },
	)

	elapsed := time.Since(start)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("unexpected error: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("worker took too long to exit: %v", elapsed)
	}
}

// trackingLimiter wraps a ProviderRateLimiter and records its identity.
type trackingLimiter struct {
	contract.ProviderRateLimiter
	id int
}

func TestRunEventsWithProxyCreatesIsolatedWorkerDependencies(t *testing.T) {
	// Set up two active proxies so two goroutines can each acquire one.
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	for _, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
			Protocol: "http", IP: ip, Port: 8080, Source: "test",
		})
	}
	for _, id := range []int64{1, 2} {
		if !repo.SeedSuccess(id, 100) {
			t.Fatalf("failed to seed proxy evidence for id %d", id)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	// Publish two jobs so each goroutine gets one.
	for range 2 {
		q.Publish(context.Background(), contract.QueueJob{
			Type:    censushandler.EventIDSweep,
			Payload: []byte(`{}`),
		})
	}

	// Track limiter instances per goroutine.
	var mu sync.Mutex
	// goroutineKey → set of limiter IDs seen in factories.
	limiterPerGoroutine := make(map[string]map[int]bool)
	// All distinct limiter instances.
	allLimiters := make(map[int]contract.ProviderRateLimiter)
	limiterCounter := 0

	newRateLimiter := func() contract.ProviderRateLimiter {
		mu.Lock()
		defer mu.Unlock()
		limiterCounter++
		tl := &trackingLimiter{ProviderRateLimiter: mock.NewProviderRateLimiter(), id: limiterCounter}
		allLimiters[limiterCounter] = tl
		return tl
	}

	// Use the proxy URL as a goroutine key (each goroutine gets a unique proxy).
	newLodestoneClient := func(proxyURL string, limiter contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
		mu.Lock()
		if tl, ok := limiter.(*trackingLimiter); ok {
			if limiterPerGoroutine[proxyURL] == nil {
				limiterPerGoroutine[proxyURL] = make(map[int]bool)
			}
			limiterPerGoroutine[proxyURL][tl.id] = true
		}
		mu.Unlock()
		return &fakeLodestoneClient{}, nil
	}
	var jobsProcessed int32
	newHandlers := func(_ contract.LodestoneClient, limiter contract.ProviderRateLimiter) *censushandler.Registry {
		mu.Lock()
		if tl, ok := limiter.(*trackingLimiter); ok {
			allLimiters[tl.id] = tl
		}
		mu.Unlock()
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, &countingCensusHandler{count: &jobsProcessed})
		return reg
	}

	w := newTestCensusWorker(q, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Cancel once both jobs are processed.
	go func() {
		for {
			if atomic.LoadInt32(&jobsProcessed) >= 2 {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	err := w.RunEventsWithProxy(
		ctx,
		[]string{censushandler.EventIDSweep},
		2,
		"test",
		proxyHub,
		newHandlers,
		newLodestoneClient,
		newRateLimiter,
	)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunEventsWithProxy: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Require exactly 2 distinct limiter instances (one per goroutine).
	if len(allLimiters) != 2 {
		t.Errorf("expected 2 distinct ProviderRateLimiter instances, got %d", len(allLimiters))
	}

	// Each goroutine must have used exactly one limiter consistently across all factories.
	for proxyURL, ids := range limiterPerGoroutine {
		if len(ids) != 1 {
			t.Errorf("goroutine for proxy %s used %d distinct limiters, want 1", proxyURL, len(ids))
		}
	}
}

func TestWaitForProxy_ExponentialBackoff(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := newTestCensusWorker(nil, logger)
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()

	// Insert a proxy after a short delay to measure how long waitForProxy waited.
	go func() {
		time.Sleep(1 * time.Second)
		id, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
			Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "test",
		})
		if err != nil {
			t.Errorf("InsertIfAbsent: %v", err)
			return
		}
		if !repo.SeedSuccess(id, 100) {
			t.Error("failed to seed proxy evidence")
		}
	}()

	p, err := w.waitForProxy(ctx, "test-owner", proxyHub)
	if err != nil {
		t.Fatalf("waitForProxy: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}

	elapsed := time.Since(start)
	// With exponential backoff starting at 5s, the first retry is at 5s.
	// A proxy inserted at 1s won't be seen until the next backoff fires.
	if elapsed < 4*time.Second {
		t.Errorf("waitForProxy returned too quickly (%v), expected at least 4s due to backoff", elapsed)
	}
	if elapsed > 8*time.Second {
		t.Errorf("waitForProxy took too long (%v), expected < 8s", elapsed)
	}
}

func TestWaitForProxy_NotificationChannel(t *testing.T) {
	repo := repository.NewFakeProxyRepository(contract.ProxyScanPolicy{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := newTestCensusWorker(nil, logger)
	proxyHub := proxydomain.NewProxyHub(repo, 5*time.Minute, 0, contract.ProxyScanPolicy{})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Insert and activate a proxy before starting waitForProxy.
	id, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.2.3.4", Port: 8080, Source: "test",
	})
	if err != nil {
		t.Fatalf("InsertIfAbsent: %v", err)
	}
	if !repo.SeedSuccess(id, 100) {
		t.Fatal("failed to seed proxy evidence")
	}

	// Lock all proxies so waitForProxy blocks.
	_, err = repo.ClaimProxy(context.Background(), "other-owner", 5*time.Minute)
	if err != nil {
		t.Fatalf("claim proxy: %v", err)
	}

	start := time.Now()

	type result struct {
		proxy *proxydomain.Proxy
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		p, err := w.waitForProxy(ctx, "test-owner", proxyHub)
		ch <- result{p, err}
	}()

	// Give waitForProxy time to enter the select.
	time.Sleep(100 * time.Millisecond)

	// Release the proxy and notify.
	err = repo.ReleaseProxy(context.Background(), id, "other-owner")
	if err != nil {
		t.Fatalf("release proxy: %v", err)
	}
	proxyHub.NotifyAvailable()

	select {
	case r := <-ch:
		elapsed := time.Since(start)
		if r.err != nil {
			t.Fatalf("waitForProxy: %v", r.err)
		}
		if r.proxy == nil {
			t.Fatal("expected proxy, got nil")
		}
		if elapsed > 2*time.Second {
			t.Errorf("waitForProxy took %v after notification, expected < 2s", elapsed)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for proxy")
	}
}

// workerPolicy mirrors the production [proxy.scan] defaults so consumer
// failure transitions classify as inactive rather than instantly dead.
var workerPolicy = contract.ProxyScanPolicy{
	VerifyInterval:    time.Minute,
	FreshnessTTL:      2 * time.Minute,
	RecoveryBase:      time.Minute,
	RecoveryCap:       15 * time.Minute,
	RecoveryHorizon:   time.Hour,
	LeaseDuration:     30 * time.Second,
	MinRepeatInterval: time.Second,
	InconclusiveRetry: time.Minute,
	DeadAfter:         48 * time.Hour,
	FailThreshold:     3,
}

// scriptedHandler returns the n-th scripted result for the n-th call.
type scriptedHandler struct {
	calls  int32
	script func(call int) error
}

func (h *scriptedHandler) Handle(context.Context, []byte) ([]contract.QueueJob, error) {
	n := int(atomic.AddInt32(&h.calls, 1))
	return nil, h.script(n)
}

func typedJobErr(kind contract.ProxyCheckKind, retryAfter time.Duration) error {
	return fmt.Errorf("request: %w", &contract.ProxyCheckError{
		Kind:       kind,
		Reason:     "status",
		RetryAfter: retryAfter,
		Err:        errors.New("destination failure"),
	})
}

func TestTargetErrorIsATypedCause(t *testing.T) {
	err := fmt.Errorf("request: %w", &contract.ProxyCheckError{
		Kind: contract.CheckTarget, Reason: "http_503",
		Err: errors.New("Service Unavailable"),
	})
	var e *contract.ProxyCheckError
	if !errors.As(err, &e) {
		t.Fatal("lost typed cause")
	}
	if classifyProxyFailure(err) == proxyFailureDead {
		t.Fatal("target 503 killed general health")
	}
}

// runOneJob runs a single-goroutine proxy worker against a queue holding one
// job and returns the handler call count once the loop exits.
func runOneJob(t *testing.T, hub *proxydomain.ProxyHub, script func(call int) error) (*int32, error) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	w := newTestCensusWorker(q, logger)
	if err := q.Publish(context.Background(), contract.QueueJob{
		Type:    censushandler.EventIDSweep,
		Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	handler := &scriptedHandler{script: script}
	handlers := func(contract.LodestoneClient, contract.ProviderRateLimiter) *censushandler.Registry {
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, handler)
		return reg
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.RunEventsWithProxy(
			ctx,
			[]string{censushandler.EventIDSweep},
			1,
			"test",
			hub,
			handlers,
			func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
				return &fakeLodestoneClient{}, nil
			},
			func() contract.ProviderRateLimiter { return nil },
		)
	}()
	select {
	case err := <-errCh:
		return &handler.calls, err
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("worker did not exit in time")
		return nil, nil
	}
}

func seedTwoFreshProxies(t *testing.T, repo *repository.FakeProxyRepository) {
	t.Helper()
	lowUptime, highUptime := 10.0, 90.0
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "1.1.1.1", Port: 8080, Source: "test", UptimePercent: &highUptime,
	})
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "2.2.2.2", Port: 8080, Source: "test", UptimePercent: &lowUptime,
	})
	if !repo.SeedSuccess(1, 100) || !repo.SeedSuccess(2, 100) {
		t.Fatal("failed to seed proxy evidence")
	}
}

// TestConsumerJob_GeneralFailurePersistsAndReplacesProxy proves the
// first-attempt path: a typed CheckProxy failure is persisted through
// RecordConsumerFailure and the proxy is replaced before one retry.
func TestConsumerJob_GeneralFailurePersistsAndReplacesProxy(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedTwoFreshProxies(t, repo)
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, workerPolicy)

	calls, err := runOneJob(t, hub, func(call int) error {
		if call == 1 {
			return typedJobErr(contract.CheckProxy, 0)
		}
		return nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("RunEventsWithProxy: %v", err)
	}
	if got := atomic.LoadInt32(calls); got < 2 {
		t.Fatalf("expected the job retried through a replacement, got %d handler calls", got)
	}

	dead, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if dead.GeneralHealthy || dead.FailCount != 1 || dead.RecoveryStep != 1 {
		t.Errorf("expected persisted general failure on first proxy, got healthy=%v fail_count=%d step=%d",
			dead.GeneralHealthy, dead.FailCount, dead.RecoveryStep)
	}
	if dead.LockedBy != nil {
		t.Errorf("expected failed proxy claim released, still locked by %v", *dead.LockedBy)
	}
}

// TestConsumerJob_RetryGeneralFailureRecordsEveryProxyTried proves the retry
// path: a typed CheckProxy failure on every replacement proxy is persisted
// too (three proxies are tried per job) and each claim is released before the
// delivery returns to the queue.
func TestConsumerJob_RetryGeneralFailureRecordsEveryProxyTried(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedTwoFreshProxies(t, repo)
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "3.3.3.3", Port: 8080, Source: "test",
	})
	if !repo.SeedSuccess(3, 100) {
		t.Fatal("failed to seed proxy evidence")
	}
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, workerPolicy)

	calls, err := runOneJob(t, hub, func(call int) error {
		return typedJobErr(contract.CheckProxy, 0)
	})
	if err == nil {
		t.Fatal("expected the job error to reach the queue")
	}
	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) || checkErr.Kind != contract.CheckProxy {
		t.Fatalf("expected typed proxy failure returned, got %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("expected exactly three handler calls, got %d", got)
	}
	for id := int64(1); id <= 3; id++ {
		rec, err := repo.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("repo.Get(%d): %v", id, err)
		}
		if rec.GeneralHealthy || rec.FailCount != 1 {
			t.Errorf("proxy %d: expected persisted general failure, got healthy=%v fail_count=%d",
				id, rec.GeneralHealthy, rec.FailCount)
		}
		if rec.LockedBy != nil {
			t.Errorf("proxy %d: expected claim released, still locked by %v", id, *rec.LockedBy)
		}
	}
}

// TestConsumerJob_TargetErrorCooldownsWithoutRotation proves destination
// errors cool the proxy down without identity rotation: the job returns as a
// normal retry, no failure is recorded, and general evidence is preserved.
func TestConsumerJob_TargetErrorCooldownsWithoutRotation(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedTwoFreshProxies(t, repo)
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, workerPolicy)

	calls, err := runOneJob(t, hub, func(call int) error {
		return typedJobErr(contract.CheckTarget, 90*time.Second)
	})
	if err == nil {
		t.Fatal("expected the job error to reach the queue")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected no rotation retry for a target error, got %d handler calls", got)
	}
	rec, err := repo.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if !rec.GeneralHealthy || rec.FailCount != 0 {
		t.Errorf("expected general evidence preserved, got healthy=%v fail_count=%d", rec.GeneralHealthy, rec.FailCount)
	}
	if rec.DestinationCooldownUntil == nil {
		t.Fatal("expected a destination cooldown to be recorded")
	}
}

// TestConsumerJob_BusinessErrorStaysOrdinary proves untyped business errors
// are neither proxy deaths nor cooldowns: no evidence write, no rotation.
func TestConsumerJob_BusinessErrorStaysOrdinary(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedTwoFreshProxies(t, repo)
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, workerPolicy)

	calls, err := runOneJob(t, hub, func(call int) error {
		// An ordinary untyped business error (e.g. an upstream 429 text).
		return errors.New("upstream api rate limit exceeded (HTTP 429)")
	})
	if err == nil {
		t.Fatal("expected the job error to reach the queue")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected no rotation for an ordinary job error, got %d handler calls", got)
	}
	for id := int64(1); id <= 2; id++ {
		rec, err := repo.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("repo.Get(%d): %v", id, err)
		}
		if !rec.GeneralHealthy || rec.FailCount != 0 || rec.DestinationCooldownUntil != nil {
			t.Errorf("proxy %d: expected evidence and cooldowns untouched, got healthy=%v fail_count=%d cooldown=%v",
				id, rec.GeneralHealthy, rec.FailCount, rec.DestinationCooldownUntil)
		}
	}
}

// recordingLodestoneClient counts CloseIdleConnections calls.
type recordingLodestoneClient struct {
	fakeLodestoneClient
	closed int32
}

func (c *recordingLodestoneClient) CloseIdleConnections() { atomic.AddInt32(&c.closed, 1) }

// TestProxyWorkerLoop_SwapClosesReplacedClientIdleConnections proves a swapped
// proxy's client gives up its idle tunnel at the swap, and the surviving
// client does the same when the worker exits.
func TestProxyWorkerLoop_SwapClosesReplacedClientIdleConnections(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedTwoFreshProxies(t, repo)
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, time.Minute, workerPolicy)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	q := &fakeProxyQueue{}
	w := newTestCensusWorker(q, logger)
	if err := q.Publish(context.Background(), contract.QueueJob{Type: censushandler.EventIDSweep, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	retried := make(chan struct{})
	handler := &scriptedHandler{script: func(call int) error {
		if call == 1 {
			return typedJobErr(contract.CheckProxy, 0)
		}
		close(retried)
		return nil
	}}
	handlers := func(contract.LodestoneClient, contract.ProviderRateLimiter) *censushandler.Registry {
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, handler)
		return reg
	}

	var mu sync.Mutex
	var clients []*recordingLodestoneClient
	newClient := func(string, contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
		c := &recordingLodestoneClient{}
		mu.Lock()
		clients = append(clients, c)
		mu.Unlock()
		return c, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- w.RunEventsWithProxy(ctx, []string{censushandler.EventIDSweep}, 1, "test", hub, handlers, newClient,
			func() contract.ProviderRateLimiter { return nil })
	}()

	select {
	case <-retried:
	case <-time.After(5 * time.Second):
		t.Fatal("job was not retried through a replacement proxy")
	}
	mu.Lock()
	if len(clients) != 2 {
		mu.Unlock()
		t.Fatalf("expected two clients (original and replacement), got %d", len(clients))
	}
	replaced, current := clients[0], clients[1]
	mu.Unlock()
	if got := atomic.LoadInt32(&replaced.closed); got != 1 {
		t.Errorf("expected the replaced client's idle connections closed once at the swap, got %d", got)
	}
	if got := atomic.LoadInt32(&current.closed); got != 0 {
		t.Errorf("expected the live client left open while the worker runs, got %d closes", got)
	}

	cancel()
	<-done
	if got := atomic.LoadInt32(&current.closed); got != 1 {
		t.Errorf("expected the live client's idle connections closed on worker exit, got %d", got)
	}
}
