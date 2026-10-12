package proxy

import (
	"context"
	"errors"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ProxyHub manages proxy acquisition for worker goroutines.
// Each goroutine calls NewProxy to atomically claim a proxy from the pool.
// Proxies are tested against the destination before handout, revalidated for
// ownership and general freshness after the check, and every rejected row is
// persisted through the typed failure or destination-cooldown paths.
type ProxyHub struct {
	repo         contract.ProxyRepository
	lockTTL      time.Duration
	checker      contract.ProxyChecker
	destCooldown time.Duration // floor for destination cooldowns without a Retry-After
	destCap      time.Duration // ceiling of the per-failure doubling; <= floor disables growth
	policy       contract.ProxyScanPolicy
	notify       chan struct{} // buffered(1); signals waiting workers when a proxy becomes available
	// refreshInterval is the most often a worker re-validates and extends the
	// lock of a proxy it holds. It is always below half of lockTTL.
	refreshInterval time.Duration
}

// DefaultLockRefreshInterval is how often a worker re-validates the lock of a
// held proxy when nothing else is configured. For short lock TTLs the hub
// lowers it to a third of the TTL.
const DefaultLockRefreshInterval = 100 * time.Second

// NewProxyHub creates a ProxyHub with the given repository, lock TTL,
// optional destination checker, destination cooldown floor and scan policy.
func NewProxyHub(repo contract.ProxyRepository, lockTTL time.Duration, checker contract.ProxyChecker, destCooldown time.Duration, policy contract.ProxyScanPolicy) *ProxyHub {
	return &ProxyHub{
		repo: repo, lockTTL: lockTTL, checker: checker, destCooldown: destCooldown, policy: policy,
		notify:          make(chan struct{}, 1),
		refreshInterval: min(DefaultLockRefreshInterval, lockTTL/3),
	}
}

// WithLockRefreshInterval sets how often a worker re-validates the lock of a
// held proxy. A value that is not positive or not below half of the lock TTL
// could let the lock expire between refreshes, so it is ignored and the
// current interval stays. It returns the hub for chaining at construction.
func (h *ProxyHub) WithLockRefreshInterval(d time.Duration) *ProxyHub {
	if d > 0 && d < h.lockTTL/2 {
		h.refreshInterval = d
	}
	return h
}

// WithDestinationCooldownCap sets the ceiling the destination cooldown grows
// to while a proxy keeps failing (floor * 2^consecutive failures). A cap at or
// below the floor keeps the cooldown flat. It returns the hub for chaining.
func (h *ProxyHub) WithDestinationCooldownCap(cap time.Duration) *ProxyHub {
	h.destCap = cap
	return h
}

// CooldownFor returns the destination cooldown to apply to p for its next
// failure: the floor doubled once per consecutive destination failure already
// recorded on the row, capped, and never shorter than the Retry-After hint.
func (h *ProxyHub) CooldownFor(p *Proxy, hint time.Duration) time.Duration {
	return DestinationBackoff(h.destCooldown, h.destCap, hint, p.Record().DestinationFailures)
}

// NewProxy atomically claims an available proxy for the given owner, tests it
// against the destination and revalidates ownership and general freshness
// before returning. Returns nil (no error) when no proxy is available or the
// claimed row is rejected; the caller (waitForProxy) retries with backoff.
//
// Rejection handling is typed: a conclusive *ProxyCheckError CheckProxy
// failure is persisted with the version captured before the check, a
// destination rejection (CheckTarget) raises the destination cooldown with
// the Retry-After hint, and local/inconclusive errors release the claim
// without touching evidence. Database failures propagate as errors — a row is
// never silently handed out when its state cannot be validated — and release
// the claim first so no proxy stays locked until the TTL.
func (h *ProxyHub) NewProxy(ctx context.Context, owner string) (*Proxy, error) {
	rec, err := h.repo.ClaimProxy(ctx, owner, h.lockTTL)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}

	p := New(rec, h.repo, h.policy)
	if h.checker == nil {
		return p, nil
	}

	_, err = h.checker.Check(ctx, rec.Protocol, rec.IP, rec.Port)
	if err == nil {
		// The check took time: ownership or general freshness may have been
		// lost while it ran. RefreshConsumerLock validates both (plus the
		// destination cooldown) and extends the lock; it never renews the
		// general evidence itself, so destination success cannot refresh the
		// ipify health window.
		current, err := h.repo.RefreshConsumerLock(ctx, rec.ID, owner, h.lockTTL)
		if err != nil {
			releaseClaim(p, owner)
			return nil, err
		}
		if current == nil {
			releaseClaim(p, owner)
			return nil, nil
		}
		return New(current, h.repo, h.policy), nil
	}

	var checkErr *contract.ProxyCheckError
	if !errors.As(err, &checkErr) {
		// Unknown checker failure: give up the claim without any evidence
		// write; the next acquisition attempt re-checks the row.
		releaseClaim(p, owner)
		return nil, nil
	}
	switch checkErr.Kind {
	case contract.CheckProxy:
		// Conclusive proxy death. The persisted failure is fenced on the
		// version captured before the check, so a scanner completion in
		// between rejects it instead of being overwritten.
		if _, err := p.MarkFailed(ctx, owner, h.lockTTL); err != nil {
			releaseClaim(p, owner)
			return nil, err
		}
		releaseClaim(p, owner)
		return nil, nil
	case contract.CheckTarget:
		// Destination rejection (403/429/5xx): raise the destination cooldown
		// to at least the Retry-After hint; general evidence is untouched.
		if err := p.CooldownDestination(ctx, owner, h.lockTTL, h.CooldownFor(p, checkErr.RetryAfter)); err != nil {
			releaseClaim(p, owner)
			return nil, err
		}
		return nil, nil
	default:
		// CheckLocal / CheckDeadline / CheckCancelled are inconclusive on
		// this path: release without failure.
		releaseClaim(p, owner)
		return nil, nil
	}
}

// releaseClaim gives up the acquisition claim with a bounded context so a
// cancelled caller cannot leak the lock. Release errors are ignored: the
// lock expires by TTL and the row stays unclaimable through other predicates.
func releaseClaim(p *Proxy, owner string) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Release(releaseCtx, owner)
}

// NotifyAvailable signals one waiting worker that a proxy may be available.
// Non-blocking: if no worker is waiting, the signal is dropped (next backoff
// timer will catch it). Safe to call from any goroutine.
func (h *ProxyHub) NotifyAvailable() {
	select {
	case h.notify <- struct{}{}:
	default:
	}
}

// WaitAvailable blocks until a proxy-available signal arrives or ctx is done.
// Returns ctx.Err() if the context is cancelled before a signal arrives.
func (h *ProxyHub) WaitAvailable(ctx context.Context) error {
	select {
	case <-h.notify:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NotifyCh returns the notification channel. Workers select on this alongside
// their backoff timer to wake immediately when a proxy becomes available.
func (h *ProxyHub) NotifyCh() <-chan struct{} {
	return h.notify
}

// LockTTL returns the lock TTL configured for this hub.
func (h *ProxyHub) LockTTL() time.Duration {
	return h.lockTTL
}

// LockRefreshInterval returns the most often a worker should re-validate and
// extend the lock of a proxy it holds.
func (h *ProxyHub) LockRefreshInterval() time.Duration {
	return h.refreshInterval
}

// DestinationCooldown returns the floor applied to destination cooldowns
// when a destination rejection carries no usable Retry-After hint.
func (h *ProxyHub) DestinationCooldown() time.Duration {
	return h.destCooldown
}

// RandomActive returns an unlocked random active proxy for public provider scraping.
// This method must not be used for Lodestone APIs; Lodestone workers must use
// NewProxy so proxies are checked and atomically owner-locked.
func (h *ProxyHub) RandomActive(ctx context.Context) (*Proxy, error) {
	rec, err := h.repo.RandomActive(ctx, nil)
	if err != nil || rec == nil {
		return nil, err
	}
	return New(rec, h.repo, h.policy), nil
}

// RandomActiveExcluding returns an unlocked random active proxy, excluding the
// given IDs. This method must not be used for Lodestone APIs; Lodestone workers
// must use NewProxy so proxies are checked and atomically owner-locked.
func (h *ProxyHub) RandomActiveExcluding(ctx context.Context, excludeIDs []int64) (*Proxy, error) {
	rec, err := h.repo.RandomActive(ctx, excludeIDs)
	if err != nil || rec == nil {
		return nil, err
	}
	return New(rec, h.repo, h.policy), nil
}

// SwapActive returns an unlocked random active proxy different from current.
// This method must not be used for Lodestone APIs; Lodestone workers must use
// NewProxy so proxies are checked and atomically owner-locked.
func (h *ProxyHub) SwapActive(ctx context.Context, current *Proxy) (*Proxy, error) {
	if current == nil {
		return h.RandomActive(ctx)
	}
	excludeID := current.Record().ID
	rec, err := h.repo.RandomActive(ctx, []int64{excludeID})
	if err != nil || rec == nil {
		return nil, err
	}
	return New(rec, h.repo, h.policy), nil
}
