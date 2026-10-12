package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// ProxyRepository is a PostgreSQL implementation of contract.ProxyRepository
// and contract.ProxyScanStore. The policy carries the scheduler settings
// storage needs (freshness TTL, lease duration, recovery horizon); storage
// converts relative durations to absolute timestamps from database time.
type ProxyRepository struct {
	driver contract.DatabaseDriver
	policy contract.ProxyScanPolicy
}

// Storage defaults for unset policy fields so a zero ProxyScanPolicy keeps
// behaving sanely until configuration wiring lands.
const (
	defaultFreshnessTTL    = 2 * time.Minute
	defaultLeaseDuration   = 30 * time.Second
	defaultRecoveryHorizon = time.Hour
)

// normalizeScanPolicy fills the policy fields storage evaluates with
// config-free defaults when they are unset.
func normalizeScanPolicy(p contract.ProxyScanPolicy) contract.ProxyScanPolicy {
	if p.FreshnessTTL <= 0 {
		p.FreshnessTTL = defaultFreshnessTTL
	}
	if p.LeaseDuration <= 0 {
		p.LeaseDuration = defaultLeaseDuration
	}
	if p.RecoveryHorizon <= 0 {
		p.RecoveryHorizon = defaultRecoveryHorizon
	}
	return p
}

func NewProxyRepository(driver contract.DatabaseDriver, policy contract.ProxyScanPolicy) *ProxyRepository {
	return &ProxyRepository{driver: driver, policy: normalizeScanPolicy(policy)}
}

const proxyColumns = `id, protocol, ip, port, country, anonymity, latency_ms, uptime_percent,
	status, last_scanned_at, last_alive_at, first_seen_at, source, fail_count, locked_by, locked_at, created_at, updated_at,
	general_healthy, last_completed_at, last_verified_at, next_attempt_at, scan_not_before, recovery_step,
	observation_version, scan_token, scan_lease_until, claimed_version, destination_cooldown_until,
	retired_at, last_listed_at`

// proxyColumnsClaimed mirrors proxyColumns with the p alias used by the
// claiming UPDATE ... RETURNING in listForScan and ClaimScans.
const proxyColumnsClaimed = `p.id, p.protocol, p.ip, p.port, p.country, p.anonymity, p.latency_ms, p.uptime_percent,
	p.status, p.last_scanned_at, p.last_alive_at, p.first_seen_at, p.source, p.fail_count, p.locked_by, p.locked_at, p.created_at, p.updated_at,
	p.general_healthy, p.last_completed_at, p.last_verified_at, p.next_attempt_at, p.scan_not_before, p.recovery_step,
	p.observation_version, p.scan_token, p.scan_lease_until, p.claimed_version, p.destination_cooldown_until,
	p.retired_at, p.last_listed_at`

// freshPredicate is the single general-health availability predicate shared
// by ListActive, RandomActive, CountByStatus, ClaimProxy and
// RefreshConsumerLock: healthy AND verified within the freshness TTL. $N is
// the TTL in seconds; n is database time taken inside the statement.
const freshPredicate = `general_healthy AND last_verified_at > statement_timestamp() - ($%d * interval '1 second')`

func scanProxy(row rowScanner) (*contract.ProxyRecord, error) {
	var p contract.ProxyRecord
	var country, anonymity, lockedBy, scanToken sql.NullString
	var latencyMS sql.NullInt64
	var uptimePercent sql.NullFloat64
	var lastScannedAt, lastAliveAt, lockedAt sql.NullTime
	var lastCompletedAt, lastVerifiedAt, nextAttemptAt, scanNotBefore sql.NullTime
	var scanLeaseUntil, destinationCooldownUntil sql.NullTime
	var retiredAt, lastListedAt sql.NullTime
	var claimedVersion sql.NullInt64

	// Keep this column order exactly in sync with proxyColumns and
	// proxyColumnsClaimed — a mismatch only surfaces at runtime row scans.
	err := row.Scan(
		&p.ID, &p.Protocol, &p.IP, &p.Port,
		&country, &anonymity, &latencyMS, &uptimePercent,
		&p.Status, &lastScannedAt, &lastAliveAt, &p.FirstSeenAt,
		&p.Source, &p.FailCount, &lockedBy, &lockedAt,
		&p.CreatedAt, &p.UpdatedAt,
		&p.GeneralHealthy, &lastCompletedAt, &lastVerifiedAt, &nextAttemptAt,
		&scanNotBefore, &p.RecoveryStep, &p.ObservationVersion, &scanToken,
		&scanLeaseUntil, &claimedVersion, &destinationCooldownUntil,
		&retiredAt, &lastListedAt,
	)
	if err != nil {
		return nil, err
	}

	p.Country = sqlStringPtr(country)
	p.Anonymity = sqlStringPtr(anonymity)
	p.LatencyMS = sqlIntPtr(latencyMS)
	p.UptimePercent = sqlFloat64Ptr(uptimePercent)
	p.LastScannedAt = sqlTimePtr(lastScannedAt)
	p.LastAliveAt = sqlTimePtr(lastAliveAt)
	p.LockedBy = sqlStringPtr(lockedBy)
	p.LockedAt = sqlTimePtr(lockedAt)
	p.LastCompletedAt = sqlTimePtr(lastCompletedAt)
	p.LastVerifiedAt = sqlTimePtr(lastVerifiedAt)
	p.NextAttemptAt = sqlTimePtr(nextAttemptAt)
	p.ScanNotBefore = sqlTimePtr(scanNotBefore)
	p.ScanToken = sqlStringPtr(scanToken)
	p.ScanLeaseUntil = sqlTimePtr(scanLeaseUntil)
	p.ClaimedVersion = sqlInt64Ptr(claimedVersion)
	p.DestinationCooldownUntil = sqlTimePtr(destinationCooldownUntil)
	p.RetiredAt = sqlTimePtr(retiredAt)
	p.LastListedAt = sqlTimePtr(lastListedAt)
	return &p, nil
}

func (r *ProxyRepository) Exists(ctx context.Context, protocol, ip string, port int) (bool, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return false, err
	}
	var exists bool
	err = db.QueryRowContext(
		ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM proxies
			WHERE protocol = $1 AND ip = $2 AND port = $3
		)`,
		protocol, ip, port,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("proxy exists: %w", err)
	}
	return exists, nil
}

func (r *ProxyRepository) InsertIfAbsent(ctx context.Context, rec contract.ProxyRecord) (int64, bool, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return 0, false, err
	}

	now := time.Now().UTC()
	var newID int64
	err = db.QueryRowContext(
		ctx,
		`INSERT INTO proxies (protocol, ip, port, country, anonymity, latency_ms, uptime_percent,
			status, last_scanned_at, last_alive_at, first_seen_at, source, fail_count, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (protocol, ip, port) DO NOTHING
		RETURNING id`,
		rec.Protocol, rec.IP, rec.Port,
		nullableString(rec.Country), nullableString(rec.Anonymity),
		nullableInt(rec.LatencyMS), nullableFloat64(rec.UptimePercent),
		contract.ProxyStatusInactive, nil, nil,
		now, rec.Source, 0, now, now,
	).Scan(&newID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Another delivery already inserted this tuple.
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("proxy insert if absent: %w", err)
	}
	return newID, true, nil
}

const (
	// listedRewriteInterval is the shortest time between two writes to the
	// same row from MarkListed.
	listedRewriteInterval = time.Hour
	// markListedChunk bounds the endpoints one statement handles, so a very
	// long provider list becomes several short transactions.
	markListedChunk = 5000
)

// MarkListed stamps last_listed_at and revives retired rows for endpoints a
// provider listed again. Rows stamped within listedRewriteInterval are left
// alone and rows locked by a concurrent scan are skipped, so discovery never
// waits on the scanner and writes each row at most once per hour. Unknown
// endpoints are ignored.
func (r *ProxyRepository) MarkListed(ctx context.Context, endpoints []contract.ProxyEndpoint) (contract.ListedResult, error) {
	var total contract.ListedResult
	for start := 0; start < len(endpoints); start += markListedChunk {
		end := min(start+markListedChunk, len(endpoints))
		res, err := r.markListedChunk(ctx, endpoints[start:end])
		if err != nil {
			return total, err
		}
		total.Marked += res.Marked
		total.Revived += res.Revived
	}
	return total, nil
}

func (r *ProxyRepository) markListedChunk(ctx context.Context, endpoints []contract.ProxyEndpoint) (contract.ListedResult, error) {
	protocols := make([]string, len(endpoints))
	ips := make([]string, len(endpoints))
	ports := make([]int32, len(endpoints))
	for i, e := range endpoints {
		protocols[i], ips[i], ports[i] = e.Protocol, e.IP, int32(e.Port)
	}
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return contract.ListedResult{}, err
	}
	var res contract.ListedResult
	err = db.QueryRowContext(ctx, `WITH input AS (
			SELECT DISTINCT protocol, ip, port
			FROM unnest($1::text[], $2::text[], $3::int[]) AS t(protocol, ip, port)
		), target AS (
			SELECT p.id, p.retired_at IS NOT NULL AS was_retired
			FROM proxies p
			JOIN input i ON p.protocol = i.protocol AND p.ip = i.ip AND p.port = i.port
			WHERE p.last_listed_at IS NULL
			   OR p.last_listed_at <= statement_timestamp() - ($4 * interval '1 second')
			FOR UPDATE OF p SKIP LOCKED
		), upd AS (
			UPDATE proxies p
			SET last_listed_at = statement_timestamp(),
				retired_at = NULL,
				updated_at = statement_timestamp()
			FROM target t
			WHERE p.id = t.id
			RETURNING t.was_retired
		)
		SELECT count(*), count(*) FILTER (WHERE was_retired) FROM upd`,
		protocols, ips, ports, listedRewriteInterval.Seconds()).Scan(&res.Marked, &res.Revived)
	if err != nil {
		return contract.ListedResult{}, fmt.Errorf("proxy mark listed: %w", err)
	}
	return res, nil
}

func (r *ProxyRepository) Get(ctx context.Context, id int64) (*contract.ProxyRecord, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies WHERE id = $1`, id)
	p, err := scanProxy(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("proxy get: %w", err)
	}
	return p, nil
}

// ListActive returns fresh healthy proxies ordered by latency. Availability
// is general evidence (healthy AND verified within the freshness TTL), never
// the historical status column.
func (r *ProxyRepository) ListActive(ctx context.Context, limit int) ([]contract.ProxyRecord, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies
		WHERE `+fmt.Sprintf(freshPredicate, 2)+`
		ORDER BY latency_ms ASC NULLS LAST
		LIMIT $1`, limit, r.policy.FreshnessTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("proxy list active: %w", err)
	}
	defer rows.Close()

	var proxies []contract.ProxyRecord
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, fmt.Errorf("proxy active row: %w", err)
		}
		proxies = append(proxies, *p)
	}
	return proxies, rows.Err()
}

func (r *ProxyRepository) Count(ctx context.Context) (int64, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	var count int64
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxies`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("proxy count: %w", err)
	}
	return count, nil
}

// CountByStatus classifies rows by general evidence: active means the fresh
// predicate holds; a retired row counts as retired whatever its stored status;
// a historically active but non-fresh row counts as stale. The historical
// inactive/dead categories are preserved for the remaining rows.
func (r *ProxyRepository) CountByStatus(ctx context.Context) (map[string]int64, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT CASE
			WHEN `+fmt.Sprintf(freshPredicate, 1)+` THEN 'active'
			WHEN retired_at IS NOT NULL THEN 'retired'
			WHEN status = 'active' THEN 'stale'
			ELSE status
		END AS bucket, COUNT(*)
		FROM proxies GROUP BY 1`, r.policy.FreshnessTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("proxy count by status: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int64)
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, fmt.Errorf("proxy count by status row: %w", err)
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

// ClaimProxy claims one fresh, unlocked, protocol-supported proxy for a
// consumer (FOR UPDATE SKIP LOCKED). Availability uses the same general
// freshness predicate as every other read; a live destination cooldown
// excludes the row here (and only here). The historical stale fallback is
// gone: a non-fresh proxy is never handed to a consumer.
func (r *ProxyRepository) ClaimProxy(ctx context.Context, owner string, lockTTL time.Duration) (*contract.ProxyRecord, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("proxy claim begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is a no-op after commit

	row := tx.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies
		WHERE `+fmt.Sprintf(freshPredicate, 1)+`
			AND protocol IN ('http', 'https', 'socks4', 'socks5')
			AND (locked_at IS NULL OR locked_at < statement_timestamp() - ($2 * interval '1 second'))
			AND (destination_cooldown_until IS NULL OR destination_cooldown_until <= statement_timestamp())
		ORDER BY uptime_percent DESC NULLS LAST, latency_ms ASC NULLS LAST, fail_count ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, r.policy.FreshnessTTL.Seconds(), lockTTL.Seconds())

	p, err := scanProxy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("proxy claim: %w", err)
	}

	lockRow := tx.QueryRowContext(ctx,
		`UPDATE proxies SET locked_by = $1, locked_at = statement_timestamp(), updated_at = statement_timestamp()
		WHERE id = $2
		RETURNING `+proxyColumns, owner, p.ID)
	p, err = scanProxy(lockRow)
	if err != nil {
		return nil, fmt.Errorf("proxy claim lock: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("proxy claim commit: %w", err)
	}
	return p, nil
}

func (r *ProxyRepository) ReleaseProxy(ctx context.Context, id int64, owner string) error {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx,
		`UPDATE proxies SET locked_by = NULL, locked_at = NULL, updated_at = $1
		WHERE id = $2 AND locked_by = $3`,
		now, id, owner)
	if err != nil {
		return fmt.Errorf("proxy release: %w", err)
	}
	return nil
}

// RandomActive returns a random fresh healthy proxy for provider discovery,
// honoring ID exclusions and protocol support. It applies no destination
// cooldown filter — cooldowns gate consumers, not discovery — and does not
// claim or lock the row.
func (r *ProxyRepository) RandomActive(ctx context.Context, excludeIDs []int64) (*contract.ProxyRecord, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	row := db.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies
		WHERE `+fmt.Sprintf(freshPredicate, 2)+`
		  AND protocol IN ('http', 'https', 'socks4', 'socks5')
		  AND (cardinality(COALESCE($1::bigint[], '{}'::bigint[])) = 0
		       OR id <> ALL(COALESCE($1::bigint[], '{}'::bigint[])))
		ORDER BY RANDOM()
		LIMIT 1`,
		excludeIDs, r.policy.FreshnessTTL.Seconds())
	rec, err := scanProxy(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("proxy random active: %w", err)
	}
	return rec, nil
}
