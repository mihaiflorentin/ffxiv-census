package proxy

import (
	"context"
	"fmt"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// Proxy wraps a ProxyRecord with domain behavior for the consumer lifecycle:
// ownership/freshness revalidation, fenced failure persistence and
// destination cooldowns. The scan policy drives the relative failure update
// the store applies.
type Proxy struct {
	record *contract.ProxyRecord
	repo   contract.ProxyRepository
	policy contract.ProxyScanPolicy
}

// New creates a Proxy domain object from a record, repository and the scan
// policy that governs consumer failure transitions.
func New(record *contract.ProxyRecord, repo contract.ProxyRepository, policy contract.ProxyScanPolicy) *Proxy {
	return &Proxy{record: record, repo: repo, policy: policy}
}

// ID returns the proxy ID.
func (p *Proxy) ID() int64 { return p.record.ID }

// Protocol returns the proxy protocol (http, https, socks4, socks5).
func (p *Proxy) Protocol() string { return p.record.Protocol }

// IP returns the proxy IP address.
func (p *Proxy) IP() string { return p.record.IP }

// Port returns the proxy port.
func (p *Proxy) Port() int { return p.record.Port }

// Address returns the full proxy address as protocol://ip:port.
func (p *Proxy) Address() string {
	return fmt.Sprintf("%s://%s:%d", p.record.Protocol, p.record.IP, p.record.Port)
}

// LatencyMS returns the proxy latency in milliseconds, or nil if unknown.
func (p *Proxy) LatencyMS() *int { return p.record.LatencyMS }

// Status returns the proxy status.
func (p *Proxy) Status() string { return p.record.Status }

// LockedBy returns the owner holding the lock, or nil if unlocked.
func (p *Proxy) LockedBy() *string { return p.record.LockedBy }

// LockedAt returns the time the lock was acquired, or nil if unlocked.
func (p *Proxy) LockedAt() *time.Time { return p.record.LockedAt }

// Record returns the underlying ProxyRecord.
func (p *Proxy) Record() *contract.ProxyRecord { return p.record }

// CanUse revalidates the proxy against the database via RefreshConsumerLock:
// general freshness, ownership, lock validity and destination cooldown. On
// success it extends the lock and stores the returned record, so the caller
// keeps the current observation version for the job duration. It returns
// false when any validation fails or the row was lost to another consumer.
func (p *Proxy) CanUse(ctx context.Context, owner string, lockTTL time.Duration) (bool, error) {
	fresh, err := p.repo.RefreshConsumerLock(ctx, p.record.ID, owner, lockTTL)
	if err != nil {
		return false, fmt.Errorf("refresh consumer lock: %w", err)
	}
	if fresh == nil {
		return false, nil
	}
	p.record = fresh
	return true, nil
}

// MarkFailed persists a conclusive consumer-observed proxy failure. The
// failure is fenced on the record captured before the job's I/O, the owner
// and a still-valid lock; a stale observation is rejected with (false, nil)
// and leaves newer evidence untouched. The lock is not released here — the
// caller owns release on the accepted path.
func (p *Proxy) MarkFailed(ctx context.Context, owner string, lockTTL time.Duration) (bool, error) {
	return p.repo.RecordConsumerFailure(ctx, *p.record, owner, lockTTL, MakeConsumerFailureUpdate(p.policy, *p.record))
}

// CooldownDestination raises the proxy's destination cooldown to at least
// now+delay and releases the owned claim without touching general evidence.
// It is the consumer response to destination rejections such as 403/429/5xx.
func (p *Proxy) CooldownDestination(ctx context.Context, owner string, lockTTL time.Duration, delay time.Duration) error {
	return p.repo.CooldownDestination(ctx, p.record.ID, owner, lockTTL, delay)
}

// ResetDestinationFailures clears the consecutive destination failure streak
// after a success. The store skips the write when the streak is already zero,
// and the in-memory record follows so the caller can skip the call too.
func (p *Proxy) ResetDestinationFailures(ctx context.Context, owner string) error {
	if err := p.repo.ResetDestinationFailures(ctx, p.record.ID, owner); err != nil {
		return err
	}
	p.record.DestinationFailures = 0
	return nil
}

// Release releases the lock on this proxy.
func (p *Proxy) Release(ctx context.Context, owner string) error {
	if err := p.repo.ReleaseProxy(ctx, p.record.ID, owner); err != nil {
		return err
	}
	p.record.LockedBy = nil
	p.record.LockedAt = nil
	return nil
}
