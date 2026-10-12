package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	censushandler "github.com/mihaiflorentin88/ffxiv-census/domain/census/handler"
	proxydomain "github.com/mihaiflorentin88/ffxiv-census/domain/proxy"
	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

const (
	retryFloor = time.Minute
	retryCap   = 15 * time.Minute
)

// retryRun records one in-process retry scenario.
type retryRun struct {
	err   error
	addrs []string // proxy address used by each handler call, in order
}

// runRetryJob drives one job through a single proxy-mode goroutine. script
// receives the 1-based call number and the proxy address the call went
// through. attempts <= 0 keeps the worker default.
func runRetryJob(t *testing.T, repo *repository.FakeProxyRepository, attempts int, script func(call int, addr string) error) retryRun {
	t.Helper()
	hub := proxydomain.NewProxyHub(repo, 5*time.Minute, nil, retryFloor, workerPolicy).
		WithDestinationCooldownCap(retryCap)
	q := &fakeProxyQueue{}
	w := newTestCensusWorker(q, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if attempts > 0 {
		w = w.WithProxyAttempts(attempts)
	}
	if err := q.Publish(context.Background(), contract.QueueJob{Type: censushandler.EventIDSweep, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	var mu sync.Mutex
	var run retryRun
	calls := 0
	done := make(chan struct{})
	handlers := func(client contract.LodestoneClient, _ contract.ProviderRateLimiter) *censushandler.Registry {
		addr := client.(*addrClient).addr
		reg := censushandler.NewRegistry()
		reg.Register(censushandler.EventIDSweep, handlerFunc(func() error {
			mu.Lock()
			calls++
			n := calls
			run.addrs = append(run.addrs, addr)
			mu.Unlock()
			err := script(n, addr)
			if err == nil {
				defer close(done)
			}
			return err
		}))
		return reg
	}
	newClient := func(addr string, _ contract.ProviderRateLimiter) (contract.LodestoneClient, error) {
		return &addrClient{addr: addr}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.RunEventsWithProxy(ctx, []string{censushandler.EventIDSweep}, 1, "test", hub, handlers, newClient,
			func() contract.ProviderRateLimiter { return nil })
	}()
	// The fake queue stops consuming on a handler error and returns it; on
	// success it keeps polling, so stop it once the job completed.
	select {
	case run.err = <-errCh:
	case <-done:
		time.Sleep(50 * time.Millisecond) // let post-success bookkeeping finish
		cancel()
		<-errCh
	case <-time.After(12 * time.Second):
		t.Fatal("worker did not finish in time")
	}
	mu.Lock()
	defer mu.Unlock()
	out := run
	out.addrs = append([]string(nil), run.addrs...)
	return out
}

type addrClient struct {
	fakeLodestoneClient
	addr string
}

type handlerFunc func() error

func (f handlerFunc) Handle(context.Context, []byte) ([]contract.QueueJob, error) { return nil, f() }

func challengeErr() error {
	return fmt.Errorf("fetch character 1: %w", &contract.ProxyCheckError{
		Kind: contract.CheckTarget, Reason: "status 202", Challenge: true, Status: 202, Err: errors.New("HTTP 202"),
	})
}

func serverErr() error {
	return fmt.Errorf("fetch character 1: %w", &contract.ProxyCheckError{
		Kind: contract.CheckTarget, Reason: "status 503", Status: 503, Err: errors.New("HTTP 503"),
	})
}

func timeoutErr() error {
	return fmt.Errorf("fetch character 1: %w", &contract.ProxyCheckError{
		Kind: contract.CheckDeadline, Reason: "request timeout", Err: context.DeadlineExceeded,
	})
}

func seedFreshProxies(t *testing.T, repo *repository.FakeProxyRepository, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		uptime := float64(100 - i) // deterministic best-first order
		if _, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
			Protocol: "http", IP: fmt.Sprintf("9.9.9.%d", i), Port: 8080, Source: "test", UptimePercent: &uptime,
		}); err != nil {
			t.Fatalf("InsertIfAbsent: %v", err)
		}
		if !repo.SeedSuccess(int64(i), 100) {
			t.Fatalf("failed to seed proxy %d", i)
		}
	}
}

func mustGet(t *testing.T, repo *repository.FakeProxyRepository, id int64) *contract.ProxyRecord {
	t.Helper()
	rec, err := repo.Get(context.Background(), id)
	if err != nil || rec == nil {
		t.Fatalf("Get(%d): %v, %v", id, rec, err)
	}
	return rec
}

func TestProxyRetry_ChallengedJobCompletesOnSecondProxy(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedFreshProxies(t, repo, 2)

	run := runRetryJob(t, repo, 0, func(call int, _ string) error {
		if call == 1 {
			return challengeErr()
		}
		return nil
	})
	if run.err != nil {
		t.Fatalf("expected the job to complete in-process, got %v", run.err)
	}
	if len(run.addrs) != 2 || run.addrs[0] == run.addrs[1] {
		t.Fatalf("expected two calls on two different proxies, got %v", run.addrs)
	}
	failed := mustGet(t, repo, 1)
	if failed.DestinationCooldownUntil == nil || failed.DestinationFailures != 1 {
		t.Errorf("challenged proxy: cooldown=%v failures=%d, want a cooldown and 1 failure", failed.DestinationCooldownUntil, failed.DestinationFailures)
	}
	if !failed.GeneralHealthy || failed.FailCount != 0 {
		t.Error("a challenge must not touch general proxy evidence")
	}
	if good := mustGet(t, repo, 2); good.DestinationCooldownUntil != nil || good.DestinationFailures != 0 {
		t.Errorf("the replacement proxy must stay clean, got cooldown=%v failures=%d", good.DestinationCooldownUntil, good.DestinationFailures)
	}
}

func TestProxyRetry_ExhaustedAfterThreeProxiesReturnsTheError(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedFreshProxies(t, repo, 4)

	run := runRetryJob(t, repo, 0, func(int, string) error { return challengeErr() })
	var checkErr *contract.ProxyCheckError
	if !errors.As(run.err, &checkErr) || !checkErr.Challenge {
		t.Fatalf("expected the typed challenge error to reach the queue, got %v", run.err)
	}
	if len(run.addrs) != 3 {
		t.Fatalf("expected exactly 3 attempts, got %v", run.addrs)
	}
	seen := map[string]bool{}
	for _, a := range run.addrs {
		if seen[a] {
			t.Fatalf("a cooled proxy was reused within the job: %v", run.addrs)
		}
		seen[a] = true
	}
	for id := int64(1); id <= 3; id++ {
		rec := mustGet(t, repo, id)
		if rec.DestinationCooldownUntil == nil || rec.LockedBy != nil {
			t.Errorf("proxy %d: cooldown=%v lockedBy=%v, want cooled and released", id, rec.DestinationCooldownUntil, rec.LockedBy)
		}
	}
	if spare := mustGet(t, repo, 4); spare.DestinationCooldownUntil != nil {
		t.Error("the fourth proxy was never used and must not be cooled")
	}
}

func TestProxyRetry_AttemptsAreConfigurable(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedFreshProxies(t, repo, 3)

	run := runRetryJob(t, repo, 1, func(int, string) error { return serverErr() })
	if run.err == nil || len(run.addrs) != 1 {
		t.Fatalf("with one attempt the job must fail without rotation, got err=%v addrs=%v", run.err, run.addrs)
	}
	if rec := mustGet(t, repo, 1); rec.DestinationCooldownUntil == nil {
		t.Error("the failed proxy is still cooled")
	}
}

func TestProxyRetry_TimeoutsAndServerErrorsRotateAndCool(t *testing.T) {
	cases := []struct {
		name string
		err  func() error
	}{
		{"timeout", timeoutErr},
		{"server error", serverErr},
		{"challenge", challengeErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := repository.NewFakeProxyRepository(workerPolicy)
			seedFreshProxies(t, repo, 2)
			run := runRetryJob(t, repo, 0, func(call int, _ string) error {
				if call == 1 {
					return tc.err()
				}
				return nil
			})
			if run.err != nil || len(run.addrs) != 2 || run.addrs[0] == run.addrs[1] {
				t.Fatalf("expected rotation to a second proxy, got err=%v addrs=%v", run.err, run.addrs)
			}
			rec := mustGet(t, repo, 1)
			if rec.DestinationCooldownUntil == nil || rec.DestinationFailures != 1 {
				t.Errorf("cooldown=%v failures=%d, want cooled with 1 failure", rec.DestinationCooldownUntil, rec.DestinationFailures)
			}
			if !rec.GeneralHealthy || rec.FailCount != 0 {
				t.Error("destination failures must not record general proxy failure")
			}
		})
	}
}

func TestProxyRetry_DialFailureMarksProxyFailedAndRotates(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedFreshProxies(t, repo, 2)
	run := runRetryJob(t, repo, 0, func(call int, _ string) error {
		if call == 1 {
			return typedJobErr(contract.CheckProxy, 0)
		}
		return nil
	})
	if run.err != nil || len(run.addrs) != 2 {
		t.Fatalf("expected completion on the second proxy, got err=%v addrs=%v", run.err, run.addrs)
	}
	if rec := mustGet(t, repo, 1); rec.GeneralHealthy || rec.FailCount != 1 {
		t.Errorf("dial failure must persist general failure, got healthy=%v fail_count=%d", rec.GeneralHealthy, rec.FailCount)
	}
}

func TestProxyRetry_GenuineOutcomesDoNotRotate(t *testing.T) {
	errs := map[string]error{
		"not found":   fmt.Errorf("fetch character 1: %w", contract.ErrCharacterNotFound),
		"private":     contract.ErrAchievementsPrivate,
		"plain error": errors.New("parse character 1: unexpected markup"),
		"rate limit":  typedJobErr(contract.CheckTarget, 30*time.Second),
	}
	for name, jobErr := range errs {
		t.Run(name, func(t *testing.T) {
			repo := repository.NewFakeProxyRepository(workerPolicy)
			seedFreshProxies(t, repo, 2)
			run := runRetryJob(t, repo, 0, func(int, string) error { return jobErr })
			if run.err == nil || len(run.addrs) != 1 {
				t.Fatalf("expected one attempt and the error returned, got err=%v addrs=%v", run.err, run.addrs)
			}
			if name != "rate limit" {
				if rec := mustGet(t, repo, 1); rec.DestinationCooldownUntil != nil || rec.DestinationFailures != 0 {
					t.Errorf("%s must not cool the proxy, got cooldown=%v failures=%d", name, rec.DestinationCooldownUntil, rec.DestinationFailures)
				}
			}
		})
	}
}

func TestProxyRetry_CooldownGrowsWithConsecutiveFailures(t *testing.T) {
	// A long freshness window keeps the seeded evidence valid while the fake
	// clock jumps over the cooldowns.
	policy := workerPolicy
	policy.FreshnessTTL = 24 * time.Hour
	repo := repository.NewFakeProxyRepository(policy)
	clock := time.Now().UTC().Truncate(time.Second)
	repo.Now = func() time.Time { return clock }
	seedFreshProxies(t, repo, 1)

	// Three consecutive failed jobs on the only proxy, each after the
	// previous cooldown ended: 1 min, 2 min, 4 min.
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		run := runRetryJob(t, repo, 1, func(int, string) error { return challengeErr() })
		if run.err == nil {
			t.Fatalf("failure %d: expected an error", i+1)
		}
		rec := mustGet(t, repo, 1)
		if got := rec.DestinationCooldownUntil.Sub(clock); got != want {
			t.Fatalf("failure %d: cooldown %s, want %s", i+1, got, want)
		}
		if rec.DestinationFailures != i+1 {
			t.Fatalf("failure %d: counter %d", i+1, rec.DestinationFailures)
		}
		clock = clock.Add(want)
	}
}

func TestProxyRetry_SuccessResetsTheFailureStreak(t *testing.T) {
	repo := repository.NewFakeProxyRepository(workerPolicy)
	seedFreshProxies(t, repo, 1)
	// Two earlier failures, cooldowns long over.
	for range 2 {
		rec, err := repo.ClaimProxy(context.Background(), "seed", time.Minute)
		if err != nil || rec == nil {
			t.Fatalf("ClaimProxy: %v, %v", rec, err)
		}
		if err := repo.CooldownDestination(context.Background(), 1, "seed", time.Minute, time.Nanosecond); err != nil {
			t.Fatalf("CooldownDestination: %v", err)
		}
	}
	if got := mustGet(t, repo, 1).DestinationFailures; got != 2 {
		t.Fatalf("setup: %d failures, want 2", got)
	}

	run := runRetryJob(t, repo, 0, func(int, string) error { return nil })
	if run.err != nil {
		t.Fatalf("job: %v", run.err)
	}
	if got := mustGet(t, repo, 1).DestinationFailures; got != 0 {
		t.Errorf("failures %d after a successful job, want 0", got)
	}
}
