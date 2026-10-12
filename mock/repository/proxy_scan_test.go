package repository

import (
	"context"
	"testing"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// fakeClock is a controllable time source injected through Now.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func fakePolicy() contract.ProxyScanPolicy {
	return contract.ProxyScanPolicy{
		VerifyInterval:    time.Minute,
		FreshnessTTL:      2 * time.Minute,
		RecoveryBase:      time.Minute,
		RecoveryCap:       15 * time.Minute,
		RecoveryHorizon:   time.Hour,
		LeaseDuration:     30 * time.Second,
		MinRepeatInterval: time.Second,
		InconclusiveRetry: time.Minute,
		DeadAfter:         48 * time.Hour,
		FailThreshold:     5,
	}
}

// newFakeRepo builds a fake bound to a controllable clock, seeded at a fixed
// instant so freshness boundaries are exact.
func newFakeRepo() (*FakeProxyRepository, *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	repo := NewFakeProxyRepository(fakePolicy())
	repo.Now = clock.Now
	return repo, clock
}

func fakeUpdate(outcome contract.ScanOutcome, next, repeat time.Duration) contract.ScanUpdate {
	return contract.ScanUpdate{
		Outcome:       outcome,
		LatencyMS:     42,
		NextDelay:     next,
		RepeatDelay:   repeat,
		RecoveryStep:  0,
		FreshnessTTL:  2 * time.Minute,
		DeadAfter:     48 * time.Hour,
		FailThreshold: 5,
	}
}

func mustFakeInsert(t *testing.T, f *FakeProxyRepository, ip string) int64 {
	t.Helper()
	id, inserted, err := f.InsertIfAbsent(context.Background(), contract.ProxyRecord{
		Protocol: "http", IP: ip, Port: 9000, Source: "test",
	})
	if err != nil || !inserted {
		t.Fatalf("insert %s: id=%d inserted=%v err=%v", ip, id, inserted, err)
	}
	return id
}

// fakeMutate applies mut to a copy of the record and stores it back.
func fakeMutate(f *FakeProxyRepository, id int64, mut func(*contract.ProxyRecord)) {
	p := f.proxies[id]
	mut(&p)
	f.proxies[id] = p
}

func mustFakeClaim(t *testing.T, f *FakeProxyRepository, queue contract.ScanQueue, limit int) []contract.ScanLease {
	t.Helper()
	leases, err := f.ClaimScans(context.Background(), queue, limit)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return leases
}

func TestFakeClaimScans_ValidatesQueueLimitStampsLease(t *testing.T) {
	f, _ := newFakeRepo()
	ctx := context.Background()

	if _, err := f.ClaimScans(ctx, contract.ScanBackground, 0); err == nil {
		t.Error("limit 0 must be an error, never unlimited")
	}
	if _, err := f.ClaimScans(ctx, contract.ScanBackground, -1); err == nil {
		t.Error("negative limit must be an error")
	}
	if _, err := f.ClaimScans(ctx, contract.ScanQueue(42), 1); err == nil {
		t.Error("unknown queue must be an error")
	}

	id := mustFakeInsert(t, f, "10.0.0.1")
	leases := mustFakeClaim(t, f, contract.ScanBackground, 1)
	if len(leases) != 1 {
		t.Fatalf("claim returned %d leases, want 1", len(leases))
	}
	lease := leases[0]
	if lease.Record.ID != id {
		t.Fatalf("claimed proxy %d, want %d", lease.Record.ID, id)
	}
	if lease.Token == "" {
		t.Error("lease token must not be empty")
	}
	if lease.Version != lease.Record.ObservationVersion {
		t.Errorf("lease version %d must equal claimed observation version %d",
			lease.Version, lease.Record.ObservationVersion)
	}
	if lease.Record.ScanToken == nil || *lease.Record.ScanToken != lease.Token {
		t.Error("record must carry the lease token")
	}
	if lease.Record.ScanLeaseUntil == nil ||
		!lease.Record.ScanLeaseUntil.Equal(f.Now().Add(fakePolicy().LeaseDuration)) {
		t.Errorf("lease expiry %v, want now+lease duration", lease.Record.ScanLeaseUntil)
	}

	// A leased row is never handed out twice.
	if again := mustFakeClaim(t, f, contract.ScanBackground, 5); len(again) != 0 {
		t.Fatalf("leased row claimed again: %d leases", len(again))
	}
}

func TestFakeClaimScans_TokensNeverRepeatAcrossBatches(t *testing.T) {
	f, _ := newFakeRepo()
	for i := range 4 {
		mustFakeInsert(t, f, "10.0.1."+string(rune('a'+i)))
	}
	// Each batch of 2 shares one batch token; consecutive batches differ.
	tokens := []string{}
	for range 2 {
		leases := mustFakeClaim(t, f, contract.ScanBackground, 2)
		if len(leases) != 2 {
			t.Fatalf("claim returned %d leases, want 2", len(leases))
		}
		for _, l := range leases {
			if l.Token != leases[0].Token {
				t.Fatal("rows in one batch must share the batch token")
			}
		}
		tokens = append(tokens, leases[0].Token)
	}
	if tokens[0] == tokens[1] {
		t.Fatalf("tokens repeated across batches: %s", tokens[0])
	}
}

func TestFakeClaimScans_QueueEligibilityAndOrder(t *testing.T) {
	f, clock := newFakeRepo()

	// Never-verified rows are background work only, ordered by completion
	// time NULLS FIRST then ID (both NULL here, so by ID).
	a := mustFakeInsert(t, f, "10.1.0.3")
	b := mustFakeInsert(t, f, "10.1.0.4")
	bg := mustFakeClaim(t, f, contract.ScanBackground, 5)
	if len(bg) != 2 || bg[0].Record.ID != a || bg[1].Record.ID != b {
		t.Fatalf("background claim %+v, want [%d %d]", bg, a, b)
	}

	// Row A becomes healthy through a successful completion, then fails one
	// verification: it keeps its verification stamp and is recovery-eligible.
	rowA := mustFakeInsert(t, f, "10.1.0.1")
	leaseA := mustFakeClaim(t, f, contract.ScanBackground, 1)
	if len(leaseA) != 1 || leaseA[0].Record.ID != rowA {
		t.Fatalf("background claim %+v, want proxy %d", leaseA, rowA)
	}
	if _, err := f.CompleteScan(context.Background(), leaseA[0],
		fakeUpdate(contract.ScanSuccess, 2*time.Second, time.Second)); err != nil {
		t.Fatalf("success completion: %v", err)
	}
	clock.advance(2 * time.Second)

	// Row B is verified healthy and due for its next verification.
	rowB := mustFakeInsert(t, f, "10.1.0.2")
	leaseB := mustFakeClaim(t, f, contract.ScanBackground, 1)
	if len(leaseB) != 1 || leaseB[0].Record.ID != rowB {
		t.Fatalf("background claim %+v, want proxy %d", leaseB, rowB)
	}
	if _, err := f.CompleteScan(context.Background(), leaseB[0],
		fakeUpdate(contract.ScanSuccess, 2*time.Second, time.Second)); err != nil {
		t.Fatalf("success completion: %v", err)
	}
	clock.advance(2 * time.Second)

	// Verification claims both healthy rows.
	ver := mustFakeClaim(t, f, contract.ScanVerification, 5)
	if len(ver) != 2 || ver[0].Record.ID != rowA || ver[1].Record.ID != rowB {
		t.Fatalf("verification claim %+v, want [%d %d]", ver, rowA, rowB)
	}
	// Row A fails conclusively; row B goes back unused.
	failure := fakeUpdate(contract.ScanFailure, 2*time.Second, time.Second)
	failure.RecoveryStep = 1
	if _, err := f.CompleteScan(context.Background(), ver[0], failure); err != nil {
		t.Fatalf("failure completion: %v", err)
	}
	if err := f.ReleaseScan(context.Background(), ver[1]); err != nil {
		t.Fatalf("release: %v", err)
	}
	clock.advance(2 * time.Second)

	// Recovery claims only the failed-but-recently-verified row.
	rec := mustFakeClaim(t, f, contract.ScanRecovery, 5)
	if len(rec) != 1 || rec[0].Record.ID != rowA {
		t.Fatalf("recovery claim %+v, want only proxy %d", rec, rowA)
	}

	// A row whose verification age exceeds the recovery horizon drops out of
	// recovery (but stays background).
	clock.advance(2 * time.Hour)
	if rec = mustFakeClaim(t, f, contract.ScanRecovery, 5); len(rec) != 0 {
		t.Fatalf("recovery claimed a row beyond the horizon: %+v", rec)
	}
}

func TestFakeClaimScans_RespectsCommonCooldown(t *testing.T) {
	f, _ := newFakeRepo()
	id := mustFakeInsert(t, f, "10.2.0.1")
	p, err := f.Get(context.Background(), id)
	if err != nil || p == nil {
		t.Fatalf("get: %v, %v", p, err)
	}
	future := f.Now().Add(time.Hour)
	p.ScanNotBefore = &future
	f.proxies[id] = *p

	if leases := mustFakeClaim(t, f, contract.ScanBackground, 5); len(leases) != 0 {
		t.Fatalf("claim inside the repeat floor returned %d leases", len(leases))
	}
}

func TestFakeCompleteScan_TransitionsFencingAndFreshness(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.3.0.1")

	// Success transition.
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	accepted, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || !accepted {
		t.Fatalf("success: accepted=%v err=%v", accepted, err)
	}
	p := f.proxies[id]
	if !p.GeneralHealthy || p.Status != contract.ProxyStatusActive ||
		p.FailCount != 0 || p.RecoveryStep != 0 || p.ObservationVersion != 1 {
		t.Errorf("success state: %+v", p)
	}
	if p.LatencyMS == nil || *p.LatencyMS != 42 || p.LastVerifiedAt == nil {
		t.Errorf("success evidence: latency=%v verified=%v", p.LatencyMS, p.LastVerifiedAt)
	}
	if p.ScanToken != nil || p.ScanLeaseUntil != nil || p.ClaimedVersion != nil {
		t.Error("success must clear the lease fields")
	}

	// Fresh reads include the row... until exactly the freshness TTL elapses.
	if active, err := f.ListActive(ctx, 10); err != nil || len(active) != 1 {
		t.Fatalf("ListActive: %v, %d rows", err, len(active))
	}
	clock.advance(2 * time.Minute)
	if active, err := f.ListActive(ctx, 10); err != nil || len(active) != 0 {
		t.Fatalf("ListActive at the exact TTL boundary: %v, %d rows, want 0", err, len(active))
	}

	// Replay of the same lease is rejected and changes nothing.
	accepted, err = f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second))
	if err != nil || accepted {
		t.Fatalf("replay: accepted=%v err=%v", accepted, err)
	}
	if got := f.proxies[id]; got.ObservationVersion != 1 {
		t.Errorf("replay bumped version to %d", got.ObservationVersion)
	}
}

func TestFakeCompleteScan_FailureClassifiesAndReschedules(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.4.0.1")

	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	update := fakeUpdate(contract.ScanFailure, 30*time.Second, time.Second)
	update.RecoveryStep = 3
	accepted, err := f.CompleteScan(ctx, lease, update)
	if err != nil || !accepted {
		t.Fatalf("failure: accepted=%v err=%v", accepted, err)
	}
	p := f.proxies[id]
	if p.GeneralHealthy || p.Status != contract.ProxyStatusInactive ||
		p.FailCount != 1 || p.RecoveryStep != 3 || p.ObservationVersion != 1 {
		t.Errorf("failure state: healthy=%v status=%s fail=%d step=%d version=%d",
			p.GeneralHealthy, p.Status, p.FailCount, p.RecoveryStep, p.ObservationVersion)
	}
	if p.LastAliveAt != nil {
		t.Error("failure must preserve the last success time")
	}
	clock.advance(2 * time.Second) // step past the failure's repeat floor

	// Crossing the fail threshold classifies the row dead.
	fakeMutate(f, id, func(p *contract.ProxyRecord) {
		p.ScanToken, p.ScanLeaseUntil, p.ClaimedVersion = nil, nil, nil
	})
	lease2 := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	threshold := fakeUpdate(contract.ScanFailure, time.Minute, time.Second)
	threshold.FailThreshold = 2
	accepted, err = f.CompleteScan(ctx, lease2, threshold)
	if err != nil || !accepted {
		t.Fatalf("threshold failure: accepted=%v err=%v", accepted, err)
	}
	if got := f.proxies[id]; got.Status != contract.ProxyStatusDead || got.FailCount != 2 {
		t.Errorf("threshold state: status=%s fail=%d, want dead/2", got.Status, got.FailCount)
	}
}

func TestFakeCompleteScan_InconclusivePreservesEvidence(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.5.0.1")

	first := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, first, fakeUpdate(contract.ScanSuccess, 2*time.Second, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}
	before := f.proxies[id]
	clock.advance(2 * time.Second)

	second := mustFakeClaim(t, f, contract.ScanVerification, 1)
	if len(second) != 1 {
		t.Fatalf("verification claim returned %d leases", len(second))
	}
	accepted, err := f.CompleteScan(ctx, second[0], fakeUpdate(contract.ScanInconclusive, time.Minute, time.Minute))
	if err != nil || !accepted {
		t.Fatalf("inconclusive: accepted=%v err=%v", accepted, err)
	}
	after := f.proxies[id]
	if !after.GeneralHealthy || after.Status != before.Status || after.FailCount != 0 ||
		after.ObservationVersion != before.ObservationVersion || after.RecoveryStep != 0 {
		t.Errorf("inconclusive changed evidence: %+v -> %+v", before, after)
	}
	if !after.LastVerifiedAt.Equal(*before.LastVerifiedAt) {
		t.Error("inconclusive must preserve the verification time")
	}
	if after.LastCompletedAt == nil {
		t.Error("inconclusive is a real attempt and must advance completed time")
	}
}

func TestFakeReleaseScan(t *testing.T) {
	f, _ := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.6.0.1")

	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	// A stale token cannot release the lease.
	stale := lease
	stale.Token = "00000000000000000000000000000000"
	if err := f.ReleaseScan(ctx, stale); err != nil {
		t.Fatalf("stale release: %v", err)
	}
	if f.proxies[id].ScanToken == nil {
		t.Fatal("stale release cleared the lease")
	}
	// The owner releases; the row becomes claimable again.
	if err := f.ReleaseScan(ctx, lease); err != nil {
		t.Fatalf("release: %v", err)
	}
	if p := f.proxies[id]; p.ScanToken != nil || p.ScanLeaseUntil != nil || p.ClaimedVersion != nil {
		t.Fatalf("release left lease fields: %+v", p)
	}
	if again := mustFakeClaim(t, f, contract.ScanBackground, 1); len(again) != 1 {
		t.Fatal("released row is not claimable again")
	}
}

func TestFakeCountByStatus_MapsStale(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()

	// Fresh healthy row.
	fresh := mustFakeInsert(t, f, "10.7.0.1")
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}
	_ = fresh

	stale := mustFakeInsert(t, f, "10.7.0.2")
	// Historically active but never verified: stale.
	fakeMutate(f, stale, func(p *contract.ProxyRecord) { p.Status = contract.ProxyStatusActive })

	counts, err := f.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts["active"] != 1 || counts["stale"] != 1 {
		t.Fatalf("counts %v, want active=1 stale=1", counts)
	}

	// Once the fresh row ages out it maps to stale too (status still active).
	clock.advance(2 * time.Minute)
	counts, err = f.CountByStatus(ctx)
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if counts["active"] != 0 || counts["stale"] != 2 {
		t.Fatalf("boundary counts %v, want active=0 stale=2", counts)
	}
}

func TestFakeClaimProxy_RespectsCooldownAndNeverFallsBackToStale(t *testing.T) {
	f, _ := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.8.0.1")
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}

	cooldown := f.Now().Add(time.Hour)
	f.proxies[id] = func() contract.ProxyRecord { p := f.proxies[id]; p.DestinationCooldownUntil = &cooldown; return p }()

	// Consumer claims respect the cooldown.
	claimed, err := f.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || claimed != nil {
		t.Fatalf("ClaimProxy under cooldown: %v, %v", claimed, err)
	}
	// And stale rows are never claimed (no fallback).
	other := mustFakeInsert(t, f, "10.8.0.2")
	_ = other
	if claimed, err := f.ClaimProxy(ctx, "owner", time.Minute); err != nil || claimed != nil {
		t.Fatalf("ClaimProxy on nonfresh row: %v, %v", claimed, err)
	}
}

func TestFakeRefreshConsumerLock_ValidatesAndExtends(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.9.0.1")
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}
	rec, err := f.ClaimProxy(ctx, "owner", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	got, err := f.RefreshConsumerLock(ctx, id, "owner", time.Minute)
	if err != nil || got == nil || got.ObservationVersion != rec.ObservationVersion {
		t.Fatalf("RefreshConsumerLock: %v, %v", got, err)
	}
	if got, err := f.RefreshConsumerLock(ctx, id, "other", time.Minute); err != nil || got != nil {
		t.Fatalf("wrong owner refresh: %v, %v", got, err)
	}

	// Expired consumer lock is rejected.
	clock.advance(2 * time.Minute)
	if got, err := f.RefreshConsumerLock(ctx, id, "owner", time.Minute); err != nil || got != nil {
		t.Fatalf("expired lock refresh: %v, %v", got, err)
	}
}

func TestFakeRecordConsumerFailure_FencesScannerSuccess(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.10.0.1")
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, 2*time.Second, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}
	rec, err := f.ClaimProxy(ctx, "consumer", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	// Scanner claims the row for verification at the same version.
	clock.advance(2 * time.Second)
	vLease := mustFakeClaim(t, f, contract.ScanVerification, 1)[0]

	// Consumer failure wins first: bump version, invalidate the scan lease.
	update := fakeUpdate(contract.ScanFailure, 30*time.Second, time.Second)
	update.RecoveryStep = 1
	accepted, err := f.RecordConsumerFailure(ctx, *rec, "consumer", time.Minute, update)
	if err != nil || !accepted {
		t.Fatalf("RecordConsumerFailure: accepted=%v err=%v", accepted, err)
	}
	if p := f.proxies[id]; p.GeneralHealthy || p.FailCount != 1 || p.ObservationVersion != 2 {
		t.Errorf("consumer failure state: healthy=%v fail=%d version=%d",
			p.GeneralHealthy, p.FailCount, p.ObservationVersion)
	}
	// The superseded scanner lease must be rejected.
	if accepted, err := f.CompleteScan(ctx, vLease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil || accepted {
		t.Fatalf("fenced completion: accepted=%v err=%v", accepted, err)
	}
	if p := f.proxies[id]; p.GeneralHealthy || p.ObservationVersion != 2 {
		t.Error("fenced completion changed evidence")
	}
}

func TestFakeRecordConsumerFailure_StaleConsumerRejected(t *testing.T) {
	f, clock := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.11.0.1")
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, 2*time.Second, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}
	rec, err := f.ClaimProxy(ctx, "consumer", time.Minute)
	if err != nil || rec == nil {
		t.Fatalf("ClaimProxy: %v, %v", rec, err)
	}

	// Scanner verification success wins first.
	clock.advance(2 * time.Second)
	vLease := mustFakeClaim(t, f, contract.ScanVerification, 1)[0]
	if _, err := f.CompleteScan(ctx, vLease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil {
		t.Fatalf("scanner success: %v", err)
	}

	// The stale consumer is rejected; only its own valid lock is released.
	accepted, err := f.RecordConsumerFailure(ctx, *rec, "consumer", time.Minute,
		fakeUpdate(contract.ScanFailure, 30*time.Second, time.Second))
	if err != nil || accepted {
		t.Fatalf("stale failure: accepted=%v err=%v", accepted, err)
	}
	p := f.proxies[id]
	if !p.GeneralHealthy || p.FailCount != 0 || p.ObservationVersion != 2 {
		t.Errorf("stale failure changed evidence: healthy=%v fail=%d version=%d",
			p.GeneralHealthy, p.FailCount, p.ObservationVersion)
	}
	if p.LockedBy != nil {
		t.Error("rejected consumer's lock must be released")
	}
}

func TestFakeCooldownDestination(t *testing.T) {
	f, _ := newFakeRepo()
	ctx := context.Background()
	id := mustFakeInsert(t, f, "10.12.0.1")
	lease := mustFakeClaim(t, f, contract.ScanBackground, 1)[0]
	if _, err := f.CompleteScan(ctx, lease, fakeUpdate(contract.ScanSuccess, time.Minute, time.Second)); err != nil {
		t.Fatalf("success: %v", err)
	}
	if _, err := f.ClaimProxy(ctx, "owner", time.Minute); err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	version := f.proxies[id].ObservationVersion

	if err := f.CooldownDestination(ctx, id, "owner", time.Minute, time.Hour); err != nil {
		t.Fatalf("CooldownDestination: %v", err)
	}
	p := f.proxies[id]
	if p.DestinationCooldownUntil == nil || !p.DestinationCooldownUntil.Equal(f.Now().Add(time.Hour)) {
		t.Errorf("cooldown %v, want now+1h", p.DestinationCooldownUntil)
	}
	if p.LockedBy != nil {
		t.Error("cooldown must release the owned lock")
	}
	if !p.GeneralHealthy || p.ObservationVersion != version {
		t.Error("cooldown must not touch evidence or scan version")
	}

	// A longer existing cooldown wins (GREATEST). Re-lock first: the phase-1
	// cooldown would otherwise keep the consumer path closed.
	fakeMutate(f, id, func(p *contract.ProxyRecord) { p.DestinationCooldownUntil = nil })
	if _, err := f.ClaimProxy(ctx, "owner", time.Minute); err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	longer := f.Now().Add(2 * time.Hour)
	fakeMutate(f, id, func(p *contract.ProxyRecord) { p.DestinationCooldownUntil = &longer })
	if err := f.CooldownDestination(ctx, id, "owner", time.Minute, time.Hour); err != nil {
		t.Fatalf("CooldownDestination: %v", err)
	}
	p = f.proxies[id]
	if got := p.DestinationCooldownUntil; got == nil || !got.Equal(longer) {
		t.Errorf("cooldown %v shortened; GREATEST must keep %v", got, longer)
	}
	if p.LockedBy != nil {
		t.Error("cooldown must release the owned lock")
	}

	// Wrong owner does nothing.
	fakeMutate(f, id, func(p *contract.ProxyRecord) { p.DestinationCooldownUntil = nil })
	if _, err := f.ClaimProxy(ctx, "owner", time.Minute); err != nil {
		t.Fatalf("ClaimProxy: %v", err)
	}
	if err := f.CooldownDestination(ctx, id, "not-owner", time.Minute, time.Hour); err != nil {
		t.Fatalf("CooldownDestination wrong owner: %v", err)
	}
	p = f.proxies[id]
	if p.DestinationCooldownUntil != nil {
		t.Error("wrong owner must not set a cooldown")
	}
	if p.LockedBy == nil || *p.LockedBy != "owner" {
		t.Error("wrong owner must not release the lock")
	}
}
