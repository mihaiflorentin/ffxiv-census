package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

var sweepRule = contract.RetirementRule{MinFailures: 10, MinAge: 14 * 24 * time.Hour}

// seedNeverWorked stores a proxy that has failed often and is old enough to
// be retired by sweepRule.
func seedNeverWorked(t *testing.T, repo *repository.FakeProxyRepository, ip string) int64 {
	t.Helper()
	return repo.Seed(contract.ProxyRecord{
		Protocol: "http", IP: ip, Port: 8080, Source: "retire-test",
		FailCount:   12,
		FirstSeenAt: time.Now().Add(-30 * 24 * time.Hour),
	})
}

func retiredCount(t *testing.T, repo *repository.FakeProxyRepository) int64 {
	t.Helper()
	counts, err := repo.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	return counts["retired"]
}

// flakyRetirementStore delegates to a real store but fails the first
// failures calls, and counts every call.
type flakyRetirementStore struct {
	inner    contract.ProxyRetirementStore
	failures int32
	calls    atomic.Int32
}

func (s *flakyRetirementStore) RetireStale(ctx context.Context, rule contract.RetirementRule, afterID int64, batch int) (contract.RetireBatch, error) {
	n := s.calls.Add(1)
	if n <= s.failures {
		return contract.RetireBatch{}, errors.New("database unavailable")
	}
	return s.inner.RetireStale(ctx, rule, afterID, batch)
}

func TestRetirementSweep_SweepOnceRetiresEligibleProxiesInBatches(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	for i := range 23 {
		seedNeverWorked(t, repo, "10.0.0."+string(rune('a'+i)))
	}
	seedVerification(t, repo, "10.0.1.1") // a healthy proxy is left alone
	store := &flakyRetirementStore{inner: repo}

	sweep := NewRetirementSweep(store, sweepRule, time.Hour, 10, testLogger())
	sweep.pause = 0
	retired, err := sweep.SweepOnce(context.Background())
	if err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}
	if retired != 23 || retiredCount(t, repo) != 23 {
		t.Fatalf("SweepOnce retired %d (store says %d), want 23", retired, retiredCount(t, repo))
	}
	if calls := store.calls.Load(); calls < 3 {
		t.Fatalf("sweep used %d store calls, want it split into batches of 10", calls)
	}
}

func TestRetirementSweep_SweepOnceStopsOnCancellationBetweenBatches(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	for i := range 30 {
		seedNeverWorked(t, repo, "10.0.0."+string(rune('a'+i)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := &cancelAfterFirstBatch{inner: repo, cancel: cancel}

	sweep := NewRetirementSweep(store, sweepRule, time.Hour, 10, testLogger())
	sweep.pause = time.Millisecond
	retired, err := sweep.SweepOnce(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SweepOnce error = %v, want context.Canceled", err)
	}
	if retired != 10 {
		t.Fatalf("retired %d before stopping, want only the first batch of 10", retired)
	}
}

type cancelAfterFirstBatch struct {
	inner  contract.ProxyRetirementStore
	cancel context.CancelFunc
}

func (s *cancelAfterFirstBatch) RetireStale(ctx context.Context, rule contract.RetirementRule, afterID int64, batch int) (contract.RetireBatch, error) {
	res, err := s.inner.RetireStale(ctx, rule, afterID, batch)
	s.cancel()
	return res, err
}

func TestRetirementSweep_RunSweepsAtStartThenEveryInterval(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	seedNeverWorked(t, repo, "10.0.0.1")
	store := &flakyRetirementStore{inner: repo}

	sweep := NewRetirementSweep(store, sweepRule, 20*time.Millisecond, 10, testLogger())
	sweep.pause = 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- sweep.Run(ctx) }()

	waitFor(t, "initial sweep", func() bool { return retiredCount(t, repo) == 1 })
	// A proxy that reaches the thresholds later is picked up by a later pass.
	seedNeverWorked(t, repo, "10.0.0.2")
	waitFor(t, "periodic sweep", func() bool { return retiredCount(t, repo) == 2 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRetirementSweep_RunSurvivesStoreErrors(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	seedNeverWorked(t, repo, "10.0.0.1")
	store := &flakyRetirementStore{inner: repo, failures: 2}

	sweep := NewRetirementSweep(store, sweepRule, 10*time.Millisecond, 10, testLogger())
	sweep.pause = 0
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sweep.Run(ctx) }()

	waitFor(t, "sweep after store errors", func() bool { return retiredCount(t, repo) == 1 })
}

func TestRetirementSweep_RunDoesNothingWhenRetirementIsDisabled(t *testing.T) {
	repo := repository.NewFakeProxyRepository(testPolicy())
	seedNeverWorked(t, repo, "10.0.0.1")
	store := &flakyRetirementStore{inner: repo}

	sweep := NewRetirementSweep(store, contract.RetirementRule{}, 5*time.Millisecond, 10, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := sweep.Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if store.calls.Load() != 0 || retiredCount(t, repo) != 0 {
		t.Fatalf("disabled sweep touched the store (%d calls)", store.calls.Load())
	}
}

// waitFor polls cond until it holds; the deadline only fails a stuck test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
