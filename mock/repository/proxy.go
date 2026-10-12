package repository

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// FakeProxyRepository is an in-memory ProxyRepository and ProxyScanStore for
// tests. It mirrors the observable ownership, eligibility and fencing
// semantics of the PostgreSQL adapter:
//   - availability is general evidence (healthy AND verified within the
//     policy freshness TTL), never the historical status column;
//   - ClaimScans leases rows with a fresh batch token and fences completions
//     on token + captured observation version + unexpired lease;
//   - RecordConsumerFailure and CooldownDestination gate on the captured
//     version, the owner and a still-valid lock.
//
// Now is the time source (default time.Now); tests inject a controllable
// clock for deterministic freshness boundaries.
type FakeProxyRepository struct {
	mu      sync.Mutex
	proxies map[int64]contract.ProxyRecord
	nextID  int64
	policy  contract.ProxyScanPolicy
	// Now returns the current time. When nil, time.Now is used.
	Now func() time.Time
	// Rand is an optional injectable RNG for deterministic RandomActive selection.
	// When nil, a default PCG source is used.
	Rand *rand.Rand
	// ExistsErr is returned by Exists when set.
	ExistsErr error
	// InsertErr is returned by InsertIfAbsent when set.
	InsertErr error
	// ExistsCalls counts how many times Exists was called.
	ExistsCalls int
	// InsertCalls counts how many times InsertIfAbsent was called.
	InsertCalls int
}

// Defaults for unset policy fields, mirroring the PostgreSQL adapter.
const (
	defaultFakeFreshnessTTL    = 2 * time.Minute
	defaultFakeLeaseDuration   = 30 * time.Second
	defaultFakeRecoveryHorizon = time.Hour
	// fakeListedRewriteInterval mirrors the adapter's once-per-hour limit.
	fakeListedRewriteInterval = time.Hour
)

func normalizeFakePolicy(p contract.ProxyScanPolicy) contract.ProxyScanPolicy {
	if p.FreshnessTTL <= 0 {
		p.FreshnessTTL = defaultFakeFreshnessTTL
	}
	if p.LeaseDuration <= 0 {
		p.LeaseDuration = defaultFakeLeaseDuration
	}
	if p.RecoveryHorizon <= 0 {
		p.RecoveryHorizon = defaultFakeRecoveryHorizon
	}
	return p
}

func NewFakeProxyRepository(policy contract.ProxyScanPolicy) *FakeProxyRepository {
	return &FakeProxyRepository{
		proxies: make(map[int64]contract.ProxyRecord),
		policy:  normalizeFakePolicy(policy),
	}
}

func (f *FakeProxyRepository) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now().UTC()
}

// fresh reports the general availability predicate: healthy and verified
// within the policy freshness TTL.
func (f *FakeProxyRepository) fresh(p contract.ProxyRecord, now time.Time) bool {
	return p.GeneralHealthy && p.LastVerifiedAt != nil && p.LastVerifiedAt.After(now.Add(-f.policy.FreshnessTTL))
}

// SeedSuccess seeds fresh general evidence for one proxy by driving a
// successful completion through the store's claim/complete pipeline, exactly
// the way the production scanner produces it. Rows sharing the claim batch
// are released untouched. It reports whether the target row was completed.
func (f *FakeProxyRepository) SeedSuccess(id int64, latencyMS int) bool {
	ctx := context.Background()
	leases, err := f.ClaimScans(ctx, contract.ScanBackground, 1024)
	if err != nil {
		return false
	}
	target := false
	for _, lease := range leases {
		if lease.Record.ID != id {
			_ = f.ReleaseScan(ctx, lease)
			continue
		}
		ok, err := f.CompleteScan(ctx, lease, contract.ScanUpdate{
			Outcome:     contract.ScanSuccess,
			LatencyMS:   latencyMS,
			NextDelay:   0,
			RepeatDelay: 0,
		})
		target = ok && err == nil
	}
	return target
}

// Seed stores a record verbatim, keeping every field the caller sets
// (InsertIfAbsent resets status and failure history like the real insert). It
// assigns the ID and fills unset creation times, and returns the ID. Tests use
// it to arrange rows in a state only scans or time can reach.
func (f *FakeProxyRepository) Seed(rec contract.ProxyRecord) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	rec.ID = f.nextID
	now := f.now()
	if rec.Status == "" {
		rec.Status = contract.ProxyStatusInactive
	}
	if rec.FirstSeenAt.IsZero() {
		rec.FirstSeenAt = now
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	if rec.UpdatedAt.IsZero() {
		rec.UpdatedAt = now
	}
	f.proxies[rec.ID] = rec
	return rec.ID
}

func (f *FakeProxyRepository) Exists(_ context.Context, protocol, ip string, port int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ExistsCalls++
	if f.ExistsErr != nil {
		return false, f.ExistsErr
	}
	for _, p := range f.proxies {
		if p.Protocol == protocol && p.IP == ip && p.Port == port {
			return true, nil
		}
	}
	return false, nil
}

func (f *FakeProxyRepository) InsertIfAbsent(_ context.Context, rec contract.ProxyRecord) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.InsertCalls++
	if f.InsertErr != nil {
		return 0, false, f.InsertErr
	}
	for _, p := range f.proxies {
		if p.Protocol == rec.Protocol && p.IP == rec.IP && p.Port == rec.Port {
			return 0, false, nil
		}
	}
	f.nextID++
	rec.ID = f.nextID
	rec.Status = contract.ProxyStatusInactive
	rec.FailCount = 0
	now := time.Now().UTC()
	rec.CreatedAt = now
	rec.UpdatedAt = now
	if rec.FirstSeenAt.IsZero() {
		rec.FirstSeenAt = now
	}
	f.proxies[rec.ID] = rec
	return rec.ID, true, nil
}

// MarkListed mirrors the PostgreSQL adapter: known endpoints not stamped
// within the last hour get last_listed_at and lose their retirement; unknown
// endpoints are ignored and duplicates within one call count once. Rows
// leased by a scan are not skipped here, the fake has no row locks.
func (f *FakeProxyRepository) MarkListed(_ context.Context, endpoints []contract.ProxyEndpoint) (contract.ListedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	wanted := make(map[contract.ProxyEndpoint]bool, len(endpoints))
	for _, e := range endpoints {
		wanted[e] = true
	}
	var res contract.ListedResult
	for id, p := range f.proxies {
		key := contract.ProxyEndpoint{Protocol: p.Protocol, IP: p.IP, Port: p.Port}
		if !wanted[key] {
			continue
		}
		if p.LastListedAt != nil && p.LastListedAt.After(now.Add(-fakeListedRewriteInterval)) {
			continue
		}
		res.Marked++
		if p.RetiredAt != nil {
			res.Revived++
		}
		p.LastListedAt = &now
		p.RetiredAt = nil
		p.UpdatedAt = now
		f.proxies[id] = p
	}
	return res, nil
}

// RetireStale mirrors the PostgreSQL sweep step: a window of at most batch
// rows in ID order after afterID, retiring the matching rows of the window.
func (f *FakeProxyRepository) RetireStale(_ context.Context, rule contract.RetirementRule, afterID int64, batch int) (contract.RetireBatch, error) {
	if batch <= 0 {
		return contract.RetireBatch{}, fmt.Errorf("fake proxy retire: batch must be positive, got %d", batch)
	}
	if !rule.Enabled() {
		return contract.RetireBatch{NextID: afterID, Done: true}, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	var ids []int64
	for id := range f.proxies {
		if id > afterID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > batch {
		ids = ids[:batch]
	}
	res := contract.RetireBatch{NextID: afterID, Done: len(ids) == 0}
	for _, id := range ids {
		res.NextID = id
		p := f.proxies[id]
		if p.RetiredAt == nil && fakeShouldRetire(p, p.FailCount, now, rule) {
			p.RetiredAt = &now
			p.UpdatedAt = now
			f.proxies[id] = p
			res.Retired++
		}
	}
	return res, nil
}

func (f *FakeProxyRepository) Get(_ context.Context, id int64) (*contract.ProxyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.proxies[id]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

// ListActive returns fresh healthy proxies ordered by latency, mirroring the
// general-freshness availability predicate of the PostgreSQL adapter.
func (f *FakeProxyRepository) ListActive(_ context.Context, limit int) ([]contract.ProxyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	var result []contract.ProxyRecord
	for _, p := range f.proxies {
		if f.fresh(p, now) {
			result = append(result, p)
		}
	}
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			li := latencyOrMax(result[i])
			lj := latencyOrMax(result[j])
			if lj < li {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (f *FakeProxyRepository) Count(_ context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int64(len(f.proxies)), nil
}

// CountByStatus classifies by general evidence: fresh healthy rows count as
// active, historically active rows map to stale, others keep their status.
func (f *FakeProxyRepository) CountByStatus(_ context.Context) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	counts := make(map[string]int64)
	for _, p := range f.proxies {
		switch {
		case f.fresh(p, now):
			counts[contract.ProxyStatusActive]++
		case p.RetiredAt != nil:
			counts["retired"]++
		case p.Status == contract.ProxyStatusActive:
			counts["stale"]++
		default:
			counts[p.Status]++
		}
	}
	return counts, nil
}

// ClaimProxy claims the best fresh, unlocked, protocol-supported proxy for a
// consumer. A live destination cooldown excludes the row; there is no stale
// fallback. Preference order mirrors the SQL: higher uptime, lower latency,
// lower fail count.
func (f *FakeProxyRepository) ClaimProxy(_ context.Context, owner string, lockTTL time.Duration) (*contract.ProxyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	expireThreshold := now.Add(-lockTTL)

	var best *contract.ProxyRecord
	for _, p := range f.proxies {
		if !f.fresh(p, now) {
			continue
		}
		if p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks4" && p.Protocol != "socks5" {
			continue
		}
		if p.LockedAt != nil && p.LockedAt.After(expireThreshold) {
			continue
		}
		if p.DestinationCooldownUntil != nil && p.DestinationCooldownUntil.After(now) {
			continue
		}
		if best == nil {
			best = &p
			continue
		}
		pUptime := uptimeOrZero(p)
		bUptime := uptimeOrZero(*best)
		if pUptime > bUptime {
			best = &p
			continue
		}
		if pUptime < bUptime {
			continue
		}
		if latencyOrMax(p) < latencyOrMax(*best) {
			best = &p
			continue
		}
		if latencyOrMax(p) == latencyOrMax(*best) && p.FailCount < best.FailCount {
			best = &p
		}
	}
	if best == nil {
		return nil, nil
	}
	best.LockedBy = &owner
	best.LockedAt = &now
	best.UpdatedAt = now
	f.proxies[best.ID] = *best
	return best, nil
}

func (f *FakeProxyRepository) ReleaseProxy(_ context.Context, id int64, owner string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.proxies[id]
	if !ok {
		return nil
	}
	if p.LockedBy == nil || *p.LockedBy != owner {
		return nil
	}
	p.LockedBy = nil
	p.LockedAt = nil
	p.UpdatedAt = time.Now().UTC()
	f.proxies[id] = p
	return nil
}

// RandomActive returns a random fresh healthy proxy for discovery, honoring
// ID exclusions and protocol support. No destination cooldown filter and no
// lock side effects.
func (f *FakeProxyRepository) RandomActive(_ context.Context, excludeIDs []int64) (*contract.ProxyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	excludeSet := make(map[int64]bool, len(excludeIDs))
	for _, id := range excludeIDs {
		excludeSet[id] = true
	}
	var eligible []contract.ProxyRecord
	for _, p := range f.proxies {
		if !f.fresh(p, now) {
			continue
		}
		if p.Protocol != "http" && p.Protocol != "https" && p.Protocol != "socks4" && p.Protocol != "socks5" {
			continue
		}
		if excludeSet[p.ID] {
			continue
		}
		eligible = append(eligible, p)
	}
	if len(eligible) == 0 {
		return nil, nil
	}
	// Sort by ID for deterministic iteration order before random selection.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].ID < eligible[j].ID })
	r := f.Rand
	if r == nil {
		r = rand.New(rand.NewPCG(1, 2))
	}
	idx := r.IntN(len(eligible))
	rec := eligible[idx]
	return &rec, nil
}

func uptimeOrZero(p contract.ProxyRecord) float64 {
	if p.UptimePercent != nil {
		return *p.UptimePercent
	}
	return 0
}

func latencyOrMax(p contract.ProxyRecord) int {
	if p.LatencyMS == nil {
		return 999999
	}
	return *p.LatencyMS
}

// fakeLeaseKey returns the ordering key used by ClaimScans: the scheduling
// deadline for verification/recovery and the completion time for background.
func fakeLeaseKey(queue contract.ScanQueue) func(contract.ProxyRecord) *time.Time {
	if queue == contract.ScanBackground {
		return func(p contract.ProxyRecord) *time.Time { return p.LastCompletedAt }
	}
	return func(p contract.ProxyRecord) *time.Time { return p.NextAttemptAt }
}

// ClaimScans mirrors the PostgreSQL claim: queue-specific eligibility, the
// common lease/repeat predicates, a fresh batch token per claim, and lease
// stamps derived from the fake clock. Returns rows in select order.
func (f *FakeProxyRepository) ClaimScans(_ context.Context, queue contract.ScanQueue, limit int) ([]contract.ScanLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		return nil, fmt.Errorf("fake scan claim: limit must be positive, got %d", limit)
	}
	switch queue {
	case contract.ScanVerification, contract.ScanRecovery, contract.ScanBackground:
	default:
		return nil, fmt.Errorf("fake scan claim: unknown queue %d", int(queue))
	}
	now := f.now()
	var candidates []contract.ProxyRecord
	for _, p := range f.proxies {
		if p.ScanLeaseUntil != nil && p.ScanLeaseUntil.After(now) {
			continue
		}
		if p.ScanNotBefore != nil && p.ScanNotBefore.After(now) {
			continue
		}
		switch queue {
		case contract.ScanVerification:
			if !p.GeneralHealthy {
				continue
			}
			if p.NextAttemptAt != nil && p.NextAttemptAt.After(now) {
				continue
			}
		case contract.ScanRecovery:
			if p.GeneralHealthy {
				continue
			}
			if p.LastVerifiedAt == nil || !p.LastVerifiedAt.After(now.Add(-f.policy.RecoveryHorizon)) {
				continue
			}
			if p.NextAttemptAt != nil && p.NextAttemptAt.After(now) {
				continue
			}
		case contract.ScanBackground:
			if p.GeneralHealthy || p.RetiredAt != nil {
				continue
			}
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	key := fakeLeaseKey(queue)
	sort.SliceStable(candidates, func(i, j int) bool {
		ki, kj := key(candidates[i]), key(candidates[j])
		if ki == nil || kj == nil {
			if ki == nil && kj == nil {
				return candidates[i].ID < candidates[j].ID
			}
			return ki == nil
		}
		if !ki.Equal(*kj) {
			return ki.Before(*kj)
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return nil, fmt.Errorf("fake scan token: %w", err)
	}
	token := hex.EncodeToString(b)
	leaseUntil := now.Add(f.policy.LeaseDuration)
	leases := make([]contract.ScanLease, 0, len(candidates))
	for _, p := range candidates {
		version := p.ObservationVersion
		p.ScanToken = &token
		p.ScanLeaseUntil = &leaseUntil
		p.ClaimedVersion = &version
		p.UpdatedAt = now
		f.proxies[p.ID] = p
		leases = append(leases, contract.ScanLease{Record: p, Token: token, Version: version})
	}
	return leases, nil
}

// fakeFenced fetches the row a lease claims, enforcing the full fence: token,
// captured version on both columns, and an unexpired lease.
func (f *FakeProxyRepository) fakeFenced(id int64, lease contract.ScanLease, now time.Time) (contract.ProxyRecord, bool) {
	p, ok := f.proxies[id]
	if !ok {
		return contract.ProxyRecord{}, false
	}
	if p.ScanToken == nil || *p.ScanToken != lease.Token {
		return contract.ProxyRecord{}, false
	}
	if p.ClaimedVersion == nil || *p.ClaimedVersion != lease.Version || p.ObservationVersion != lease.Version {
		return contract.ProxyRecord{}, false
	}
	if p.ScanLeaseUntil == nil || !p.ScanLeaseUntil.After(now) {
		return contract.ProxyRecord{}, false
	}
	return p, true
}

// fakeClassifyFailure mirrors the historical inactive/dead thresholds.
func fakeClassifyFailure(p contract.ProxyRecord, now time.Time, update contract.ScanUpdate) (string, int) {
	newFailCount := p.FailCount + 1
	lastAlive := p.LastAliveAt
	if lastAlive == nil {
		fallback := p.FirstSeenAt
		lastAlive = &fallback
	}
	isDead := newFailCount >= update.FailThreshold || now.Sub(*lastAlive) > update.DeadAfter
	if isDead {
		return contract.ProxyStatusDead, newFailCount
	}
	return contract.ProxyStatusInactive, newFailCount
}

// fakeShouldRetire mirrors the PostgreSQL retirement predicate: the proxy
// never worked, has failed often enough and was first seen long enough ago.
func fakeShouldRetire(p contract.ProxyRecord, newFailCount int, now time.Time, rule contract.RetirementRule) bool {
	return rule.Enabled() &&
		p.LastAliveAt == nil &&
		newFailCount >= rule.MinFailures &&
		now.Sub(p.FirstSeenAt) >= rule.MinAge
}

// CompleteScan applies the fenced state transition for one outcome. A failed
// fence returns (false, nil) and leaves every field untouched.
func (f *FakeProxyRepository) CompleteScan(_ context.Context, lease contract.ScanLease, update contract.ScanUpdate) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	p, ok := f.fakeFenced(lease.Record.ID, lease, now)
	if !ok {
		return false, nil
	}
	nextAttempt := now.Add(update.NextDelay)
	notBefore := now.Add(update.RepeatDelay)
	switch update.Outcome {
	case contract.ScanSuccess:
		p.GeneralHealthy = true
		p.Status = contract.ProxyStatusActive
		p.LatencyMS = &update.LatencyMS
		p.LastAliveAt = &now
		p.LastVerifiedAt = &now
		p.LastCompletedAt = &now
		p.FailCount = 0
		p.RecoveryStep = 0
		p.NextAttemptAt = &nextAttempt
		p.ScanNotBefore = &notBefore
		p.RetiredAt = nil
		p.ObservationVersion++
	case contract.ScanFailure:
		status, newFailCount := fakeClassifyFailure(p, now, update)
		p.GeneralHealthy = false
		p.Status = status
		p.LastCompletedAt = &now
		p.FailCount = newFailCount
		p.RecoveryStep = update.RecoveryStep
		p.NextAttemptAt = &nextAttempt
		p.ScanNotBefore = &notBefore
		if p.RetiredAt == nil && fakeShouldRetire(p, newFailCount, now, update.Retirement) {
			p.RetiredAt = &now
		}
		p.ObservationVersion++
	case contract.ScanInconclusive:
		p.LastCompletedAt = &now
		p.NextAttemptAt = &nextAttempt
		p.ScanNotBefore = &notBefore
	default:
		return false, fmt.Errorf("fake scan complete: unknown outcome %d", int(update.Outcome))
	}
	p.ScanToken = nil
	p.ScanLeaseUntil = nil
	p.ClaimedVersion = nil
	p.UpdatedAt = now
	f.proxies[p.ID] = p
	return true, nil
}

// ReleaseScan clears the caller's own lease; a stale token or version cannot.
func (f *FakeProxyRepository) ReleaseScan(_ context.Context, lease contract.ScanLease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.proxies[lease.Record.ID]
	if !ok {
		return nil
	}
	if p.ScanToken == nil || *p.ScanToken != lease.Token {
		return nil
	}
	if p.ClaimedVersion == nil || *p.ClaimedVersion != lease.Version {
		return nil
	}
	now := f.now()
	p.ScanToken = nil
	p.ScanLeaseUntil = nil
	p.ClaimedVersion = nil
	p.UpdatedAt = now
	f.proxies[p.ID] = p
	return nil
}

// RefreshConsumerLock validates freshness, ownership, lock validity and
// destination cooldown, extends the lock, and returns the current record.
func (f *FakeProxyRepository) RefreshConsumerLock(_ context.Context, id int64, owner string, lockTTL time.Duration) (*contract.ProxyRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	p, ok := f.proxies[id]
	if !ok {
		return nil, nil
	}
	if p.LockedBy == nil || *p.LockedBy != owner {
		return nil, nil
	}
	if p.LockedAt == nil || !p.LockedAt.After(now.Add(-lockTTL)) {
		return nil, nil
	}
	if !f.fresh(p, now) {
		return nil, nil
	}
	if p.DestinationCooldownUntil != nil && p.DestinationCooldownUntil.After(now) {
		return nil, nil
	}
	p.LockedAt = &now
	p.UpdatedAt = now
	f.proxies[p.ID] = p
	return &p, nil
}

// RecordConsumerFailure records a conclusive consumer-side failure under a
// CAS on the captured version, the owner and a valid lock. A stale caller is
// rejected and only its own valid owned lock is released.
func (f *FakeProxyRepository) RecordConsumerFailure(_ context.Context, rec contract.ProxyRecord, owner string, lockTTL time.Duration, update contract.ScanUpdate) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	p, ok := f.proxies[rec.ID]
	if !ok {
		return false, nil
	}
	owned := p.LockedBy != nil && *p.LockedBy == owner
	lockValid := owned && p.LockedAt != nil && p.LockedAt.After(now.Add(-lockTTL))
	if !lockValid || p.ObservationVersion != rec.ObservationVersion {
		if owned && p.LockedAt != nil && p.LockedAt.After(now.Add(-lockTTL)) {
			now2 := now
			p.LockedBy = nil
			p.LockedAt = nil
			p.UpdatedAt = now2
			f.proxies[p.ID] = p
		}
		return false, nil
	}
	status, newFailCount := fakeClassifyFailure(p, now, update)
	nextAttempt := now.Add(update.NextDelay)
	notBefore := now.Add(update.RepeatDelay)
	p.GeneralHealthy = false
	p.Status = status
	p.LastCompletedAt = &now
	p.FailCount = newFailCount
	p.RecoveryStep = update.RecoveryStep
	p.NextAttemptAt = &nextAttempt
	p.ScanNotBefore = &notBefore
	p.ObservationVersion++
	p.ScanToken = nil
	p.ScanLeaseUntil = nil
	p.ClaimedVersion = nil
	p.UpdatedAt = now
	f.proxies[p.ID] = p
	return true, nil
}

// CooldownDestination raises the destination cooldown to
// GREATEST(existing, now+delay) and releases the owned consumer lock without
// touching general evidence or the scan version.
func (f *FakeProxyRepository) CooldownDestination(_ context.Context, id int64, owner string, lockTTL time.Duration, delay time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.proxies[id]
	if !ok {
		return nil
	}
	if p.LockedBy == nil || *p.LockedBy != owner {
		return nil
	}
	now := f.now()
	raised := now.Add(delay)
	if p.DestinationCooldownUntil != nil && p.DestinationCooldownUntil.After(raised) {
		raised = *p.DestinationCooldownUntil
	}
	p.DestinationCooldownUntil = &raised
	p.LockedBy = nil
	p.LockedAt = nil
	p.UpdatedAt = now
	f.proxies[p.ID] = p
	return nil
}

var (
	_ contract.ProxyScanStore       = (*FakeProxyRepository)(nil)
	_ contract.ProxyRepository      = (*FakeProxyRepository)(nil)
	_ contract.ProxyRetirementStore = (*FakeProxyRepository)(nil)
)
