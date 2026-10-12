package contract

import (
	"context"
	"time"
)

// Proxy status constants.
const (
	ProxyStatusActive   = "active"
	ProxyStatusInactive = "inactive"
	ProxyStatusDead     = "dead"
)

// ProxyRecord represents a proxy endpoint stored in the database.
type ProxyRecord struct {
	ID            int64
	Protocol      string // "http", "https", "socks4", "socks5"
	IP            string
	Port          int
	Country       *string
	Anonymity     *string // "elite", "anonymous", "transparent"
	LatencyMS     *int
	UptimePercent *float64
	Status        string // "active", "inactive", "dead"
	LastScannedAt *time.Time
	LastAliveAt   *time.Time
	FirstSeenAt   time.Time
	Source        string // provider name: "proxyscrape", "geonode", etc.
	FailCount     int
	LockedBy      *string    // process name + goroutine ID (e.g. "census-consume-g3")
	LockedAt      *time.Time // lock acquisition time
	CreatedAt     time.Time
	UpdatedAt     time.Time

	// Scan scheduling state (see proxy_scan.go). Absolute timestamps are
	// owned by the database; callers pass durations, never wall-clock
	// deadlines. GeneralHealthy records the latest conclusive result and is
	// independent of the historical Status classification.
	GeneralHealthy           bool
	LastCompletedAt          *time.Time
	LastVerifiedAt           *time.Time
	NextAttemptAt            *time.Time // verification/recovery deadline; background ignores it
	ScanNotBefore            *time.Time // common minimum-repeat/inconclusive cooldown
	RecoveryStep             int
	ObservationVersion       int64
	ScanToken                *string
	ScanLeaseUntil           *time.Time
	ClaimedVersion           *int64
	DestinationCooldownUntil *time.Time

	// DestinationFailures counts consecutive destination failures (challenge,
	// timeout, 5xx, 429) since the last success. It drives the growing
	// destination cooldown and ranks recently failing proxies behind clean ones.
	DestinationFailures int

	// RetiredAt marks a proxy that never worked and kept failing (see
	// RetirementRule). The background scan skips retired rows, which stay in
	// the table so the unique key keeps discovery from re-inserting them.
	// LastListedAt is when a provider last listed the endpoint.
	RetiredAt    *time.Time
	LastListedAt *time.Time
}

// ProxyEndpoint is the unique key of a proxy row: (protocol, ip, port).
type ProxyEndpoint struct {
	Protocol string
	IP       string
	Port     int
}

// ListedResult reports what MarkListed wrote. Marked counts the rows stamped
// (known endpoints not stamped within the last hour) and Revived counts those
// that were retired and are live again.
type ListedResult struct {
	Marked  int64
	Revived int64
}

// ProxyChecker tests whether a proxy endpoint is reachable and measures latency.
//
// Typed-error contract: a nil error means success, including complete
// response validation. A non-nil error SHOULD be a *ProxyCheckError
// (possibly wrapped) whose Kind attributes the failure; until the transport
// adapters migrate, plain errors are permitted and consumers must treat
// them as inconclusive. Error text is never parsed for decisions.
type ProxyChecker interface {
	Check(ctx context.Context, protocol, ip string, port int) (latencyMS int, err error)
}

// ProxyProvider fetches proxy lists from an external source.
type ProxyProvider interface {
	// Name returns the provider identifier (e.g. "proxyscrape", "geonode").
	Name() string
	// FetchProxies streams proxy records from the provider, calling emit for each
	// one. If emit returns an error, FetchProxies stops and returns it immediately.
	FetchProxies(ctx context.Context, emit func(ProxyRecord) error) error
}

// ProxyRepository persists proxy records.
type ProxyRepository interface {
	// Exists returns true if a proxy with the exact (protocol, ip, port) tuple
	// already exists in the database. Read-only check, no side effects.
	Exists(ctx context.Context, protocol, ip string, port int) (bool, error)
	// InsertIfAbsent inserts a new proxy only if no row with the same
	// (protocol, ip, port) exists. Returns the proxy ID, whether it was
	// actually inserted, and any error. On conflict, returns (0, false, nil)
	// without modifying the existing row.
	InsertIfAbsent(ctx context.Context, rec ProxyRecord) (id int64, inserted bool, err error)
	// MarkListed records that a provider listed the given endpoints again:
	// it stamps last_listed_at and revives retired proxies. Endpoints that are
	// not in the table are ignored (inserting is InsertIfAbsent's job), and a
	// row stamped within the last hour is left alone, so a busy provider list
	// costs at most one write per row per hour. Rows locked by a concurrent
	// scan are skipped, never waited on. The call is bulk-capable.
	MarkListed(ctx context.Context, endpoints []ProxyEndpoint) (ListedResult, error)
	// Get returns a proxy by ID, or nil (no error) if not found.
	Get(ctx context.Context, id int64) (*ProxyRecord, error)
	// ListActive returns up to limit active proxies ordered by latency (lowest first).
	ListActive(ctx context.Context, limit int) ([]ProxyRecord, error)
	// Count returns the total number of proxies.
	Count(ctx context.Context) (int64, error)
	// CountByStatus returns proxy counts grouped by status.
	CountByStatus(ctx context.Context) (map[string]int64, error)
	// ClaimProxy atomically claims an available proxy for the given owner using
	// FOR UPDATE SKIP LOCKED. Returns nil (no error) if no proxy is available.
	// The proxy must be active, not currently locked, or locked past its TTL.
	// Only proxies with protocols http, https, socks4, socks5 are considered.
	ClaimProxy(ctx context.Context, owner string, lockTTL time.Duration) (*ProxyRecord, error)
	// RefreshConsumerLock atomically validates the general-freshness
	// predicate, ownership, lock validity and destination cooldown, extends
	// the lock and returns the current record including the live observation
	// version. It returns nil when any validation fails. It replaces
	// ExtendLock for scan consumers.
	RefreshConsumerLock(ctx context.Context, id int64, owner string, lockTTL time.Duration) (*ProxyRecord, error)
	// RecordConsumerFailure persists a conclusive consumer-side failure under
	// a CAS on the captured observation version, the owner and a still-valid
	// lock. A stale caller is rejected with (false, nil); only its own valid
	// owned lock is released. The consumer lock itself is not released on the
	// accepted path — the caller owns release.
	RecordConsumerFailure(ctx context.Context, rec ProxyRecord, owner string, lockTTL time.Duration, update ScanUpdate) (bool, error)
	// CooldownDestination raises the destination cooldown to at least
	// now+delay, increments the consecutive destination failure counter and
	// releases the owned consumer lock in one statement. It touches neither
	// general evidence nor the scan version.
	CooldownDestination(ctx context.Context, id int64, owner string, lockTTL time.Duration, delay time.Duration) error
	// ResetDestinationFailures zeroes the consecutive destination failure
	// counter after a success. It is a no-op unless the caller owns the lock
	// and the counter is non-zero, and keeps the lock.
	ResetDestinationFailures(ctx context.Context, id int64, owner string) error
	// ReleaseProxy releases the lock on a proxy owned by the given owner.
	ReleaseProxy(ctx context.Context, id int64, owner string) error
	// RandomActive returns a random active proxy, optionally excluding IDs.
	// Returns nil (no error) if no eligible proxy exists. Does NOT claim or lock
	// the proxy — used only for discovery/provider scraping, never for Lodestone.
	RandomActive(ctx context.Context, excludeIDs []int64) (*ProxyRecord, error)
}
