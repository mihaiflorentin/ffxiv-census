package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// fakeChecker adapts a function to contract.ProxyChecker.
type fakeChecker func(ctx context.Context, protocol, ip string, port int) (int, error)

func (f fakeChecker) Check(ctx context.Context, protocol, ip string, port int) (int, error) {
	return f(ctx, protocol, ip, port)
}

// seedFreshProxy inserts a proxy row and drives a scanner success through the
// store pipeline so the row carries fresh general evidence.
func seedFreshProxy(t *testing.T, repo *repository.FakeProxyRepository, ip string) int64 {
	t.Helper()
	id, _, err := repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: ip, Port: 8080, Source: "test",
	})
	if err != nil {
		t.Fatalf("InsertIfAbsent: %v", err)
	}
	if !repo.SeedSuccess(id, 50) {
		t.Fatalf("failed to seed fresh evidence for %s", ip)
	}
	return id
}

func TestProxyHub_NewProxy_Success(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	seedFreshProxy(t, repo, "1.2.3.4")

	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}
	if p.Address() != "http://1.2.3.4:8080" {
		t.Fatalf("expected address http://1.2.3.4:8080, got %s", p.Address())
	}
	if p.LockedBy() == nil || *p.LockedBy() != "test-g1" {
		t.Fatal("expected proxy to be locked by test-g1")
	}
}

func TestProxyHub_NewProxy_SuccessRevalidatesOwnershipAndFreshness(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	seedFreshProxy(t, repo, "1.2.3.4")

	hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
		return 42, nil
	}), 0, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}
	// The handed-out record is the current database record: the destination
	// check's latency (42) never touched general evidence (still the seeded
	// 50) and the lock was extended by the revalidation.
	if p.Record().LatencyMS == nil || *p.Record().LatencyMS != 50 {
		t.Fatalf("expected the database record latency 50, got %v", p.Record().LatencyMS)
	}
	current, err := repo.Get(context.Background(), p.ID())
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if p.Record().ObservationVersion != current.ObservationVersion {
		t.Fatalf("expected captured version %d, got %d", current.ObservationVersion, p.Record().ObservationVersion)
	}
	if p.LockedBy() == nil || *p.LockedBy() != "test-g1" {
		t.Fatal("expected proxy to be locked by test-g1 after revalidation")
	}
	if !current.GeneralHealthy {
		t.Fatal("expected general evidence preserved by the destination check")
	}
}

// TestNewProxy_Destination429CooldownsRowWithoutGeneralFailure is the
// successful-general-check + destination-429 scenario: no proxy is returned,
// general evidence and counts are preserved, and no other Lodestone consumer
// can claim the row until the destination cooldown expires.
func TestNewProxy_Destination429CooldownsRowWithoutGeneralFailure(t *testing.T) {
	repo, clock := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	before, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}

	hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
		return 0, &contract.ProxyCheckError{
			Kind:       contract.CheckTarget,
			Reason:     "status 429",
			RetryAfter: 90 * time.Second,
			Err:        errors.New("Too Many Requests"),
		}
	}), time.Minute, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy after destination 429")
	}

	after, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	// General evidence untouched: no failure count, no version bump.
	if after.FailCount != before.FailCount {
		t.Errorf("expected fail_count unchanged at %d, got %d", before.FailCount, after.FailCount)
	}
	if after.ObservationVersion != before.ObservationVersion {
		t.Errorf("expected observation version unchanged at %d, got %d", before.ObservationVersion, after.ObservationVersion)
	}
	if !after.GeneralHealthy {
		t.Error("expected general health preserved")
	}
	// The destination cooldown uses the Retry-After (90s > 60s floor).
	if after.DestinationCooldownUntil == nil || !after.DestinationCooldownUntil.Equal(clock.Add(90*time.Second)) {
		t.Fatalf("expected destination cooldown until %v, got %v", clock.Add(90*time.Second), after.DestinationCooldownUntil)
	}
	// Availability counts still include the row during the cooldown.
	actives, err := repo.ListActive(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(actives) != 1 {
		t.Fatalf("expected ListActive to still report the row, got %d", len(actives))
	}
	counts, err := repo.CountByStatus(context.Background())
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts[contract.ProxyStatusActive] != 1 {
		t.Fatalf("expected 1 active in general counts, got %d", counts[contract.ProxyStatusActive])
	}
	// Another Lodestone claim must be blocked until the cooldown expires.
	claimed, err := repo.ClaimProxy(context.Background(), "test-g2", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	if claimed != nil {
		t.Fatal("expected claim to be blocked while the destination cooldown is live")
	}
	*clock = clock.Add(91 * time.Second)
	claimed, err = repo.ClaimProxy(context.Background(), "test-g2", 5*time.Minute)
	if err != nil {
		t.Fatalf("ClaimProxy after cooldown: %v", err)
	}
	if claimed == nil || claimed.ID != id {
		t.Fatal("expected the row to be claimable after the cooldown expires")
	}
}

// TestNewProxy_TransportRefusalRemovesAvailabilityAndFencesOlderScan is the
// typed conclusive-failure scenario: the refusal removes general availability,
// persists the failure transition, and rejects a scanner completion captured
// before the consumer observation.
func TestNewProxy_TransportRefusalRemovesAvailabilityAndFencesOlderScan(t *testing.T) {
	repo, _ := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")

	// A scanner leases the row before the consumer attempt.
	leases, err := repo.ClaimScans(context.Background(), contract.ScanVerification, 10)
	if err != nil {
		t.Fatalf("ClaimScans: %v", err)
	}
	if len(leases) != 1 || leases[0].Record.ID != id {
		t.Fatalf("expected a lease for the seeded row, got %d leases", len(leases))
	}

	hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
		return 0, &contract.ProxyCheckError{
			Kind:   contract.CheckProxy,
			Reason: "connection refused",
			Err:    errors.New("dial tcp: connection refused"),
		}
	}), time.Minute, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy after transport refusal")
	}

	after, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if after.GeneralHealthy {
		t.Error("expected general availability removed")
	}
	if after.FailCount != 1 {
		t.Errorf("expected fail_count 1, got %d", after.FailCount)
	}
	if after.Status != contract.ProxyStatusInactive {
		t.Errorf("expected status inactive, got %s", after.Status)
	}
	if after.LockedBy != nil {
		t.Errorf("expected claim released after failure, still locked by %v", *after.LockedBy)
	}
	actives, err := repo.ListActive(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(actives) != 0 {
		t.Fatalf("expected empty ListActive after refusal, got %d rows", len(actives))
	}

	// The scanner completion captured before the consumer failure is fenced.
	completed, err := repo.CompleteScan(context.Background(), leases[0], contract.ScanUpdate{Outcome: contract.ScanSuccess})
	if err != nil {
		t.Fatalf("CompleteScan: %v", err)
	}
	if completed {
		t.Fatal("expected older scanner completion to be rejected")
	}
	fenced, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if fenced.GeneralHealthy || fenced.FailCount != 1 {
		t.Errorf("expected consumer failure evidence preserved, got healthy=%v fail_count=%d", fenced.GeneralHealthy, fenced.FailCount)
	}
}

// TestNewProxy_ExpiredFreshnessNotHandedOutDespiteDestinationSuccess proves an
// expired general success can never be handed out even when the destination
// checker succeeds.
func TestNewProxy_ExpiredFreshnessNotHandedOutDespiteDestinationSuccess(t *testing.T) {
	repo, clock := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	before, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}

	hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
		// The check is slow: general freshness expires while it runs.
		*clock = clock.Add(3 * time.Minute)
		return 42, nil
	}), time.Minute, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy after general freshness expired during the check")
	}

	after, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if !after.GeneralHealthy || after.ObservationVersion != before.ObservationVersion {
		t.Errorf("expected evidence untouched, got healthy=%v version=%d", after.GeneralHealthy, after.ObservationVersion)
	}
	if after.LockedBy != nil {
		t.Errorf("expected claim released, still locked by %v", *after.LockedBy)
	}
}

func TestNewProxy_LocalCheckErrorReleasesWithoutFailure(t *testing.T) {
	repo, _ := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	before, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}

	hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
		return 0, &contract.ProxyCheckError{Kind: contract.CheckLocal, Reason: "build transport", Err: errors.New("unsupported protocol")}
	}), time.Minute, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy after a local checker error")
	}
	after, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if after.FailCount != before.FailCount || after.ObservationVersion != before.ObservationVersion || !after.GeneralHealthy {
		t.Error("expected evidence untouched by a local checker error")
	}
	if after.DestinationCooldownUntil != nil {
		t.Error("expected no destination cooldown from a local checker error")
	}
	if after.LockedBy != nil {
		t.Errorf("expected claim released, still locked by %v", *after.LockedBy)
	}
}

// failingRefreshRepo injects a RefreshConsumerLock database failure.
type failingRefreshRepo struct {
	*repository.FakeProxyRepository
	err error
}

func (r *failingRefreshRepo) RefreshConsumerLock(context.Context, int64, string, time.Duration) (*contract.ProxyRecord, error) {
	return nil, r.err
}

func TestNewProxy_RepositoryErrorPropagates(t *testing.T) {
	repo, _ := newClockRepo()
	seedFreshProxy(t, repo, "1.2.3.4")

	hub := NewProxyHub(&failingRefreshRepo{FakeProxyRepository: repo, err: errors.New("db down")},
		5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) { return 1, nil }),
		time.Minute, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err == nil {
		t.Fatal("expected database failure to propagate")
	}
	if p != nil {
		t.Fatal("expected no proxy when the revalidation write fails")
	}
}

var errDBDown = errors.New("db down")

// failingWriteRepo injects database failures on the writes NewProxy performs
// after a claim, so each acquisition error path can be exercised.
type failingWriteRepo struct {
	*repository.FakeProxyRepository
	failRefresh  bool
	failFailure  bool
	failCooldown bool
}

func (r *failingWriteRepo) RefreshConsumerLock(ctx context.Context, id int64, owner string, ttl time.Duration) (*contract.ProxyRecord, error) {
	if r.failRefresh {
		return nil, errDBDown
	}
	return r.FakeProxyRepository.RefreshConsumerLock(ctx, id, owner, ttl)
}

func (r *failingWriteRepo) RecordConsumerFailure(ctx context.Context, rec contract.ProxyRecord, owner string, ttl time.Duration, update contract.ScanUpdate) (bool, error) {
	if r.failFailure {
		return false, errDBDown
	}
	return r.FakeProxyRepository.RecordConsumerFailure(ctx, rec, owner, ttl, update)
}

func (r *failingWriteRepo) CooldownDestination(ctx context.Context, id int64, owner string, ttl, delay time.Duration) error {
	if r.failCooldown {
		return errDBDown
	}
	return r.FakeProxyRepository.CooldownDestination(ctx, id, owner, ttl, delay)
}

func TestNewProxy_ErrorPathsLeaveProxyUnlocked(t *testing.T) {
	tests := []struct {
		name     string
		inject   func(*failingWriteRepo)
		checkErr error
	}{
		{
			name:   "revalidation write fails after a successful check",
			inject: func(r *failingWriteRepo) { r.failRefresh = true },
		},
		{
			name:     "failure persistence fails after a conclusive proxy error",
			inject:   func(r *failingWriteRepo) { r.failFailure = true },
			checkErr: &contract.ProxyCheckError{Kind: contract.CheckProxy, Reason: "refused", Err: errors.New("refused")},
		},
		{
			name:     "cooldown write fails after a destination rejection",
			inject:   func(r *failingWriteRepo) { r.failCooldown = true },
			checkErr: &contract.ProxyCheckError{Kind: contract.CheckTarget, Reason: "429", Err: errors.New("429")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, _ := newClockRepo()
			id := seedFreshProxy(t, base, "1.2.3.4")
			repo := &failingWriteRepo{FakeProxyRepository: base}
			tc.inject(repo)
			hub := NewProxyHub(repo, 5*time.Minute, fakeChecker(func(context.Context, string, string, int) (int, error) {
				return 1, tc.checkErr
			}), time.Minute, consumerPolicy)

			p, err := hub.NewProxy(context.Background(), "test-g1")
			if !errors.Is(err, errDBDown) {
				t.Fatalf("expected the database error to propagate, got %v", err)
			}
			if p != nil {
				t.Fatal("expected no proxy on an error path")
			}
			after, err := base.Get(context.Background(), id)
			if err != nil {
				t.Fatalf("repo.Get: %v", err)
			}
			if after.LockedBy != nil {
				t.Errorf("expected claim released, still locked by %q", *after.LockedBy)
			}
		})
	}
}

func TestProxyHub_NewProxy_NoAvailable(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected nil proxy when none available")
	}
}

func TestProxyHub_NewProxy_AllLocked(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	seedFreshProxy(t, repo, "1.2.3.4")

	// Lock the proxy so no other owner can claim it.
	if _, err := repo.ClaimProxy(context.Background(), "other", 5*time.Minute); err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}

	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected nil proxy when all are locked")
	}
}

func TestProxyHub_LockTTL(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	ttl := 10 * time.Minute
	hub := NewProxyHub(repo, ttl, nil, 0, consumerPolicy)
	if hub.LockTTL() != ttl {
		t.Fatalf("expected lock TTL %v, got %v", ttl, hub.LockTTL())
	}
}

func TestProxyHub_DestinationCooldown(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	hub := NewProxyHub(repo, 5*time.Minute, nil, 90*time.Second, consumerPolicy)
	if hub.DestinationCooldown() != 90*time.Second {
		t.Fatalf("expected destination cooldown 90s, got %v", hub.DestinationCooldown())
	}
}

func TestProxyHub_RandomActive_ReturnsProxy(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	seedFreshProxy(t, repo, "1.2.3.4")

	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	p, err := hub.RandomActive(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}
}

func TestProxyHub_RandomActive_NoAvailable(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	p, err := hub.RandomActive(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected nil proxy when none available")
	}
}

func TestProxyHub_RandomActive_SkipsInactiveAndLocked(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	activeID := seedFreshProxy(t, repo, "1.2.3.4")

	// A row without fresh evidence is never eligible.
	repo.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: "5.6.7.8", Port: 8080, Source: "test",
	})

	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	for range 10 {
		p, err := hub.RandomActive(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p == nil {
			t.Fatal("expected fresh proxy, got nil")
		}
		if p.ID() != activeID {
			t.Fatalf("expected fresh proxy ID %d, got %d", activeID, p.ID())
		}
	}
}

func TestProxyHub_SwapActive_DifferentProxy(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	seedFreshProxy(t, repo, "1.2.3.4")
	seedFreshProxy(t, repo, "1.2.3.5")

	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	// Get initial proxy.
	initial, err := hub.RandomActive(context.Background())
	if err != nil || initial == nil {
		t.Fatalf("RandomActive: proxy=%v err=%v", initial, err)
	}

	// Swap until we get a different one.
	var swapped *Proxy
	for range 10 {
		swapped, err = hub.SwapActive(context.Background(), initial)
		if err != nil {
			t.Fatalf("SwapActive: %v", err)
		}
		if swapped != nil && swapped.ID() != initial.ID() {
			break
		}
	}
	if swapped == nil || swapped.ID() == initial.ID() {
		t.Fatal("expected a different proxy after swap")
	}
}

func TestProxyHub_SwapActive_NilCurrent(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	seedFreshProxy(t, repo, "1.2.3.4")

	hub := NewProxyHub(repo, 5*time.Minute, nil, 0, consumerPolicy)
	p, err := hub.SwapActive(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}
}
