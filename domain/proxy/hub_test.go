package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/mock/repository"
	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

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

	hub := NewProxyHub(repo, 5*time.Minute, 0, consumerPolicy)
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

	hub := NewProxyHub(repo, 5*time.Minute, 0, consumerPolicy)

	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected proxy, got nil")
	}
	// The handed-out record is the current database record: general evidence
	// is still the seeded 50 and the lock was extended by the revalidation.
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
		t.Fatal("expected general evidence preserved by the hand-out")
	}
}

// TestNewProxy_FirstRequestRejectionCooldownsRowWithoutGeneralFailure is the
// successful-scan + destination-429-on-first-request scenario: the hand-out
// itself contacts nothing, the consumer's cooldown call keeps general evidence
// and counts, and no other Lodestone consumer can claim the row until the
// destination cooldown expires.
func TestNewProxy_FirstRequestRejectionCooldownsRowWithoutGeneralFailure(t *testing.T) {
	repo, clock := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	before, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}

	hub := NewProxyHub(repo, 5*time.Minute, time.Minute, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil || p == nil {
		t.Fatalf("expected the fresh proxy to be handed out, got %v, %v", p, err)
	}

	// The first real request is answered 429 with Retry-After 90s.
	if err := p.CooldownDestination(context.Background(), "test-g1", hub.LockTTL(), hub.CooldownFor(p, 90*time.Second)); err != nil {
		t.Fatalf("CooldownDestination: %v", err)
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
	if after.LockedBy != nil {
		t.Errorf("expected the claim released, still locked by %q", *after.LockedBy)
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

// TestNewProxy_FirstRequestTransportRefusalRemovesAvailabilityAndFencesOlderScan
// is the typed conclusive-failure scenario: a dial failure on the first real
// request removes general availability, persists the failure transition, and
// rejects a scanner completion captured before the consumer observation.
func TestNewProxy_FirstRequestTransportRefusalRemovesAvailabilityAndFencesOlderScan(t *testing.T) {
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

	hub := NewProxyHub(repo, 5*time.Minute, time.Minute, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil || p == nil {
		t.Fatalf("expected the fresh proxy to be handed out, got %v, %v", p, err)
	}
	// The first real request fails to dial the proxy.
	if ok, err := p.MarkFailed(context.Background(), "test-g1", hub.LockTTL()); err != nil || !ok {
		t.Fatalf("MarkFailed = %v, %v, want an accepted failure", ok, err)
	}
	if err := p.Release(context.Background(), "test-g1"); err != nil {
		t.Fatalf("Release: %v", err)
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

// TestNewProxy_FreshnessOlderThanTheWindowIsNotHandedOut proves scan freshness
// is the only gate: a row whose last general verification is older than the
// freshness window is never handed out, and is back as soon as a scanner
// verifies it again.
func TestNewProxy_FreshnessOlderThanTheWindowIsNotHandedOut(t *testing.T) {
	repo, clock := newClockRepo()
	id := seedFreshProxy(t, repo, "1.2.3.4")
	hub := NewProxyHub(repo, 5*time.Minute, time.Minute, consumerPolicy)

	*clock = clock.Add(consumerPolicy.FreshnessTTL + time.Second)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy once freshness is older than the window")
	}
	stale, err := repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if stale.LockedBy != nil {
		t.Errorf("expected the row to stay unlocked, locked by %q", *stale.LockedBy)
	}

	if !seedVerificationSuccess(t, repo, id) {
		t.Fatal("failed to re-verify the row")
	}
	if p, err = hub.NewProxy(context.Background(), "test-g1"); err != nil || p == nil {
		t.Fatalf("expected the re-verified proxy to be handed out, got %v, %v", p, err)
	}
}

// postClaimRepo runs a hook right after a successful ClaimProxy, standing in
// for whatever happens to the row between the claim and the revalidation.
type postClaimRepo struct {
	*repository.FakeProxyRepository
	afterClaim func()
}

func (r *postClaimRepo) ClaimProxy(ctx context.Context, owner string, ttl time.Duration) (*contract.ProxyRecord, error) {
	rec, err := r.FakeProxyRepository.ClaimProxy(ctx, owner, ttl)
	if rec != nil && r.afterClaim != nil {
		r.afterClaim()
	}
	return rec, err
}

// TestNewProxy_PostClaimRevalidationRejectsLostFreshness proves general
// freshness that expires between the claim and the hand-out still rejects the
// row, releases the claim and leaves the evidence untouched.
func TestNewProxy_PostClaimRevalidationRejectsLostFreshness(t *testing.T) {
	base, clock := newClockRepo()
	id := seedFreshProxy(t, base, "1.2.3.4")
	before, err := base.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	repo := &postClaimRepo{FakeProxyRepository: base, afterClaim: func() { *clock = clock.Add(3 * time.Minute) }}

	hub := NewProxyHub(repo, 5*time.Minute, time.Minute, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy after general freshness expired after the claim")
	}

	after, err := base.Get(context.Background(), id)
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

// TestNewProxy_PostClaimRevalidationRejectsLostOwnership proves a claim taken
// over by another owner before the hand-out is not returned, and the new
// owner's lock is left alone.
func TestNewProxy_PostClaimRevalidationRejectsLostOwnership(t *testing.T) {
	base, clock := newClockRepo()
	id := seedFreshProxy(t, base, "1.2.3.4")
	repo := &postClaimRepo{FakeProxyRepository: base}
	repo.afterClaim = func() {
		// The lock lapses and another consumer claims the row.
		*clock = clock.Add(6 * time.Minute)
		seedVerificationSuccess(t, base, id)
		repo.afterClaim = nil
		if rec, err := base.ClaimProxy(context.Background(), "thief", 5*time.Minute); err != nil || rec == nil {
			t.Fatalf("takeover claim: %v, %v", rec, err)
		}
	}

	hub := NewProxyHub(repo, 5*time.Minute, time.Minute, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy once another owner holds the lock")
	}
	after, err := base.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if after.LockedBy == nil || *after.LockedBy != "thief" {
		t.Errorf("expected the other owner's lock to survive, got %v", after.LockedBy)
	}
}

var errDBDown = errors.New("db down")

// failingRefreshRepo injects a RefreshConsumerLock database failure.
type failingRefreshRepo struct {
	*repository.FakeProxyRepository
}

func (r *failingRefreshRepo) RefreshConsumerLock(context.Context, int64, string, time.Duration) (*contract.ProxyRecord, error) {
	return nil, errDBDown
}

// TestNewProxy_RevalidationErrorPropagatesAndLeavesProxyUnlocked proves a row
// whose state cannot be validated is never handed out and never stays locked.
func TestNewProxy_RevalidationErrorPropagatesAndLeavesProxyUnlocked(t *testing.T) {
	base, _ := newClockRepo()
	id := seedFreshProxy(t, base, "1.2.3.4")

	hub := NewProxyHub(&failingRefreshRepo{FakeProxyRepository: base}, 5*time.Minute, time.Minute, consumerPolicy)
	p, err := hub.NewProxy(context.Background(), "test-g1")
	if !errors.Is(err, errDBDown) {
		t.Fatalf("expected the database error to propagate, got %v", err)
	}
	if p != nil {
		t.Fatal("expected no proxy when the revalidation write fails")
	}
	after, err := base.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("repo.Get: %v", err)
	}
	if after.LockedBy != nil {
		t.Errorf("expected claim released, still locked by %q", *after.LockedBy)
	}
}

func TestProxyHub_NewProxy_NoAvailable(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	hub := NewProxyHub(repo, 5*time.Minute, 0, consumerPolicy)
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

	hub := NewProxyHub(repo, 5*time.Minute, 0, consumerPolicy)
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
	hub := NewProxyHub(repo, ttl, 0, consumerPolicy)
	if hub.LockTTL() != ttl {
		t.Fatalf("expected lock TTL %v, got %v", ttl, hub.LockTTL())
	}
}

func TestProxyHub_DestinationCooldown(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	hub := NewProxyHub(repo, 5*time.Minute, 90*time.Second, consumerPolicy)
	if hub.DestinationCooldown() != 90*time.Second {
		t.Fatalf("expected destination cooldown 90s, got %v", hub.DestinationCooldown())
	}
}

func TestProxyHub_LockRefreshInterval(t *testing.T) {
	repo := repository.NewFakeProxyRepository(consumerPolicy)
	newHub := func(ttl time.Duration) *ProxyHub {
		return NewProxyHub(repo, ttl, 0, consumerPolicy)
	}
	cases := []struct {
		name string
		hub  *ProxyHub
		want time.Duration
	}{
		{"default with the standard lock ttl", newHub(5 * time.Minute), 100 * time.Second},
		{"default shrinks below half of a short lock ttl", newHub(time.Minute), 20 * time.Second},
		{"configured value", newHub(5 * time.Minute).WithLockRefreshInterval(45 * time.Second), 45 * time.Second},
		{"zero keeps the default", newHub(5 * time.Minute).WithLockRefreshInterval(0), 100 * time.Second},
		{"half of the ttl is rejected", newHub(5 * time.Minute).WithLockRefreshInterval(150 * time.Second), 100 * time.Second},
	}
	for _, tc := range cases {
		if got := tc.hub.LockRefreshInterval(); got != tc.want {
			t.Errorf("%s: LockRefreshInterval = %s, want %s", tc.name, got, tc.want)
		}
	}
}
