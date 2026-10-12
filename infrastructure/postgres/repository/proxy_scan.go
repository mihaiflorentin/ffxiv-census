package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/mihaiflorentin88/ffxiv-census/port/contract"
)

// The PostgreSQL adapter serves both ports: the historical ProxyRepository
// reads and the ProxyScanStore lease pipeline. The concrete constructor
// return lets callers reach either port from one adapter.
var (
	_ contract.ProxyScanStore       = (*ProxyRepository)(nil)
	_ contract.ProxyRepository      = (*ProxyRepository)(nil)
	_ contract.ProxyRetirementStore = (*ProxyRepository)(nil)
)

// newScanToken returns a fresh 128-bit hex nonce. A batch token paired with
// the row ID fences completions; tokens never repeat across batches.
func newScanToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("scan token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// claimLeaseWhere is the common lease-availability predicate shared by every
// queue: an expired or absent lease and a passed common repeat floor. The
// timestamps are taken inside the statement from database time.
const claimLeaseWhere = `(p2.scan_lease_until IS NULL OR p2.scan_lease_until <= statement_timestamp())
		  AND (p2.scan_not_before IS NULL OR p2.scan_not_before <= statement_timestamp())`

// claimQueueWhere returns the queue-specific static predicate. Caller values
// arrive only as bind parameters; queue SQL is selected, never interpolated.
// Background ignores the scheduling deadline entirely.
//
// The recovery horizon is inlined as a literal, not bound: with a bind
// parameter PostgreSQL's generic plan cannot see how selective the horizon is
// and walks idx_proxies_recovery_claim in deadline order, filtering every
// long-unverified row (138 ms and 108k buffers on production). A literal lets
// it pick the small idx_proxies_recovery_recent range instead. The value
// comes from policy, never from callers.
func claimQueueWhere(queue contract.ScanQueue, recoveryHorizon time.Duration) (string, string, bool) {
	switch queue {
	case contract.ScanVerification:
		return `p2.general_healthy
			AND (p2.next_attempt_at IS NULL OR p2.next_attempt_at <= statement_timestamp())`,
			`p2.next_attempt_at ASC NULLS FIRST, p2.id ASC`, true
	case contract.ScanRecovery:
		return fmt.Sprintf(`NOT p2.general_healthy
			AND p2.last_verified_at > statement_timestamp() - (%s * interval '1 second')
			AND (p2.next_attempt_at IS NULL OR p2.next_attempt_at <= statement_timestamp())`,
				strconv.FormatFloat(recoveryHorizon.Seconds(), 'f', -1, 64)),
			`p2.next_attempt_at ASC NULLS FIRST, p2.id ASC`, true
	case contract.ScanBackground:
		// Retired proxies stay in the table but leave the sweep; this
		// predicate must match idx_proxies_background_live exactly.
		return `NOT p2.general_healthy AND p2.retired_at IS NULL`,
			`p2.last_completed_at ASC NULLS FIRST, p2.id ASC`, true
	default:
		return "", "", false
	}
}

// ClaimScans leases up to limit scan candidates for one queue in a single
// short transaction: eligible rows are locked (SKIP LOCKED) and stamped with
// a fresh batch token, the claimed observation version, and a lease expiry
// computed from database time. Nonpositive limits are errors, never
// unlimited. Destination cooldown is deliberately not a claim predicate; it
// gates only the consumer path.
func (r *ProxyRepository) ClaimScans(ctx context.Context, queue contract.ScanQueue, limit int) ([]contract.ScanLease, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("scan claim: limit must be positive, got %d", limit)
	}
	token, err := newScanToken()
	if err != nil {
		return nil, err
	}
	query, args, err := r.claimQuery(queue, limit, token)
	if err != nil {
		return nil, err
	}

	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("scan claim: %w", err)
	}
	defer rows.Close()

	var leases []contract.ScanLease
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claim row: %w", err)
		}
		var version int64
		if p.ClaimedVersion != nil {
			version = *p.ClaimedVersion
		}
		leases = append(leases, contract.ScanLease{Record: *p, Token: token, Version: version})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan claim rows: %w", err)
	}

	// UPDATE ... RETURNING has no stable order; restore the SELECT order the
	// CTE chose (deadline NULLS FIRST, then id).
	if queue == contract.ScanBackground {
		sortScanLeases(leases, func(l contract.ScanLease) *time.Time { return l.Record.LastCompletedAt })
	} else {
		sortScanLeases(leases, func(l contract.ScanLease) *time.Time { return l.Record.NextAttemptAt })
	}
	return leases, nil
}

// claimQuery assembles the lease statement and its bind arguments for one
// queue.
func (r *ProxyRepository) claimQuery(queue contract.ScanQueue, limit int, token string) (string, []any, error) {
	where, order, ok := claimQueueWhere(queue, r.policy.RecoveryHorizon)
	if !ok {
		return "", nil, fmt.Errorf("scan claim: unknown queue %d", int(queue))
	}

	args := []any{limit, token, r.policy.LeaseDuration.Seconds()}

	query := fmt.Sprintf(`WITH chosen AS (
		SELECT p2.id
		FROM proxies p2
		WHERE %s
		  AND %s
		ORDER BY %s
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	)
	UPDATE proxies p
	SET scan_token = $2,
		scan_lease_until = clock_timestamp() + ($3 * interval '1 second'),
		claimed_version = p.observation_version,
		updated_at = clock_timestamp()
	FROM chosen
	WHERE p.id = chosen.id
	RETURNING %s`,
		claimLeaseWhere, where, order, proxyColumnsClaimed)
	return query, args, nil
}

// sortScanLeases orders leases by an optional timestamp key, NULLS FIRST,
// breaking ties — including two NULL keys — by record ID, mirroring the
// claim ORDER BY exactly.
func sortScanLeases(leases []contract.ScanLease, key func(contract.ScanLease) *time.Time) {
	sort.SliceStable(leases, func(i, j int) bool {
		ki, kj := key(leases[i]), key(leases[j])
		if ki == nil || kj == nil {
			if ki == nil && kj == nil {
				return leases[i].Record.ID < leases[j].Record.ID
			}
			return ki == nil
		}
		if !ki.Equal(*kj) {
			return ki.Before(*kj)
		}
		return leases[i].Record.ID < leases[j].Record.ID
	})
}

// fencedWhere builds the CAS predicate of a fenced completion: the lease
// token, the captured claim version matching both stored columns, and an
// unexpired lease measured against the post-lock database timestamp.
func fencedWhere(id, token, version, dbNow int) string {
	return fmt.Sprintf(`id = $%d AND scan_token = $%d AND claimed_version = $%d
		AND observation_version = $%d AND scan_lease_until > $%d`, id, token, version, version, dbNow)
}

// CompleteScan persists one fenced scan observation. The claimed row is
// locked explicitly and the single fence timestamp is read only after the
// lock is held, so time spent waiting for the lock can never make an expired
// lease look valid. A failed CAS returns (false, nil) and leaves every
// evidence column untouched; only a persistence error returns an error.
func (r *ProxyRepository) CompleteScan(ctx context.Context, lease contract.ScanLease, update contract.ScanUpdate) (bool, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("scan complete begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is a no-op after commit

	lockRow := tx.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies WHERE id = $1 FOR UPDATE`, lease.Record.ID)
	current, err := scanProxy(lockRow)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The claimed row is gone; the lease is moot.
			return false, nil
		}
		return false, fmt.Errorf("scan complete lock: %w", err)
	}

	var dbNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return false, fmt.Errorf("scan complete clock: %w", err)
	}

	var query string
	var args []any
	switch update.Outcome {
	case contract.ScanSuccess:
		// Healthy again: reset failure history, record latency and the
		// success/alive/completed stamps, schedule the next verification.
		// Deadlines are absolute timestamps derived from the post-lock
		// database time and the caller's relative delays.
		query = fmt.Sprintf(`UPDATE proxies SET
			general_healthy = TRUE,
			status = $5,
			latency_ms = $6,
			last_alive_at = $2,
			last_verified_at = $2,
			last_completed_at = $2,
			fail_count = 0,
			recovery_step = 0,
			next_attempt_at = $3,
			scan_not_before = $4,
			retired_at = NULL,
			observation_version = observation_version + 1,
			scan_token = NULL,
			scan_lease_until = NULL,
			claimed_version = NULL,
			updated_at = $2
			WHERE %s`, fencedWhere(1, 7, 8, 2))
		args = []any{
			lease.Record.ID, dbNow, dbNow.Add(update.NextDelay), dbNow.Add(update.RepeatDelay),
			contract.ProxyStatusActive, update.LatencyMS, lease.Token, lease.Version,
		}
	case contract.ScanFailure:
		// Conclusive failure: preserve last-success evidence, advance the
		// failure history, classify inactive/dead by the existing thresholds
		// evaluated with database time, schedule the next recovery attempt.
		status, newFailCount := failureStatus(current, dbNow, update)
		query = fmt.Sprintf(`UPDATE proxies SET
			general_healthy = FALSE,
			status = $5,
			last_completed_at = $2,
			fail_count = $6,
			recovery_step = $7,
			next_attempt_at = $3,
			scan_not_before = $4,
			retired_at = COALESCE(retired_at, $10),
			observation_version = observation_version + 1,
			scan_token = NULL,
			scan_lease_until = NULL,
			claimed_version = NULL,
			updated_at = $2
			WHERE %s`, fencedWhere(1, 8, 9, 2))
		// The failure that satisfies the retirement rule retires the proxy
		// in the same write; an existing retirement time is kept.
		var retiredAt any
		if shouldRetire(current, newFailCount, dbNow, update.Retirement) {
			retiredAt = dbNow
		}
		args = []any{
			lease.Record.ID, dbNow, dbNow.Add(update.NextDelay), dbNow.Add(update.RepeatDelay),
			status, newFailCount, update.RecoveryStep, lease.Token, lease.Version, retiredAt,
		}
	case contract.ScanInconclusive:
		// No evidence either way: preserve health, status, failure history,
		// version and success stamps; only reschedule and record the attempt.
		query = fmt.Sprintf(`UPDATE proxies SET
			last_completed_at = $2,
			next_attempt_at = $3,
			scan_not_before = $4,
			scan_token = NULL,
			scan_lease_until = NULL,
			claimed_version = NULL,
			updated_at = $2
			WHERE %s`, fencedWhere(1, 5, 6, 2))
		args = []any{
			lease.Record.ID, dbNow, dbNow.Add(update.NextDelay), dbNow.Add(update.RepeatDelay),
			lease.Token, lease.Version,
		}
	default:
		return false, fmt.Errorf("scan complete: unknown outcome %d", int(update.Outcome))
	}

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("scan complete: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("scan complete rows: %w", err)
	}
	if affected == 0 {
		// Superseded, replayed, or expired lease: discard the observation.
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("scan complete commit: %w", err)
	}
	return true, nil
}

// ReleaseScan clears the caller's own lease. Matching the captured claim
// version means an old process cannot clear a lease the row no longer carries.
func (r *ProxyRepository) ReleaseScan(ctx context.Context, lease contract.ScanLease) error {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE proxies
		SET scan_token = NULL,
			scan_lease_until = NULL,
			claimed_version = NULL,
			updated_at = statement_timestamp()
		WHERE id = $1 AND scan_token = $2 AND claimed_version = $3`,
		lease.Record.ID, lease.Token, lease.Version)
	if err != nil {
		return fmt.Errorf("scan release: %w", err)
	}
	return nil
}

// RefreshConsumerLock atomically validates the general-freshness predicate,
// ownership, lock validity and destination cooldown, then extends the lock.
// It returns the current record (including the live ObservationVersion) or
// nil when any validation fails. It replaces ExtendLock for scan consumers.
func (r *ProxyRepository) RefreshConsumerLock(ctx context.Context, id int64, owner string, lockTTL time.Duration) (*contract.ProxyRecord, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`UPDATE proxies
		SET locked_at = statement_timestamp(),
			updated_at = statement_timestamp()
		WHERE id = $1 AND locked_by = $2
		  AND locked_at > statement_timestamp() - ($3 * interval '1 second')
		  AND %s
		  AND (destination_cooldown_until IS NULL OR destination_cooldown_until <= statement_timestamp())
		RETURNING %s`,
		fmt.Sprintf(freshPredicate, 4), proxyColumns)
	row := db.QueryRowContext(ctx, query, id, owner, lockTTL.Seconds(), r.policy.FreshnessTTL.Seconds())
	p, err := scanProxy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("consumer lock refresh: %w", err)
	}
	return p, nil
}

// RecordConsumerFailure persists a conclusive consumer-side failure under a
// CAS on the captured ObservationVersion, the owner, and a still-valid lock.
// It applies the same failure transition as a scan failure against database
// time, invalidates any older scan lease, and advances the completed-attempt
// time because a consumer failure is a real conclusive observation. A stale
// caller is rejected with (false, nil) and only its own valid owned lock is
// released.
func (r *ProxyRepository) RecordConsumerFailure(ctx context.Context, rec contract.ProxyRecord, owner string, lockTTL time.Duration, update contract.ScanUpdate) (bool, error) {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("consumer failure begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback is a no-op after commit

	lockRow := tx.QueryRowContext(ctx,
		`SELECT `+proxyColumns+` FROM proxies WHERE id = $1 FOR UPDATE`, rec.ID)
	current, err := scanProxy(lockRow)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("consumer failure lock: %w", err)
	}
	var dbNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		return false, fmt.Errorf("consumer failure clock: %w", err)
	}

	owned := current.LockedBy != nil && *current.LockedBy == owner
	lockValid := owned && current.LockedAt != nil && current.LockedAt.After(dbNow.Add(-lockTTL))
	if !lockValid || current.ObservationVersion != rec.ObservationVersion {
		// Stale consumer: release only the caller's current valid owned lock;
		// never touch evidence.
		if _, err := tx.ExecContext(ctx, `UPDATE proxies
			SET locked_by = NULL, locked_at = NULL, updated_at = $1
			WHERE id = $2 AND locked_by = $3 AND locked_at > $4`,
			dbNow, rec.ID, owner, dbNow.Add(-lockTTL)); err != nil {
			return false, fmt.Errorf("consumer failure release: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("consumer failure commit: %w", err)
		}
		return false, nil
	}

	status, newFailCount := failureStatus(current, dbNow, update)
	if _, err := tx.ExecContext(ctx, `UPDATE proxies SET
		general_healthy = FALSE,
		status = $5,
		last_completed_at = $2,
		fail_count = $6,
		recovery_step = $7,
		next_attempt_at = $3,
		scan_not_before = $4,
		observation_version = observation_version + 1,
		scan_token = NULL,
		scan_lease_until = NULL,
		claimed_version = NULL,
		updated_at = $2
		WHERE id = $1`,
		rec.ID, dbNow, dbNow.Add(update.NextDelay), dbNow.Add(update.RepeatDelay),
		status, newFailCount, update.RecoveryStep); err != nil {
		return false, fmt.Errorf("consumer failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("consumer failure commit: %w", err)
	}
	return true, nil
}

// CooldownDestination atomically raises the destination cooldown to
// GREATEST(existing, database now + delay) and releases the owned consumer
// lock. It touches neither general evidence nor the scan version. lockTTL is
// accepted for call-site symmetry with the other consumer operations; the
// release predicate is ownership only, so an expired lock held by the same
// owner is still cleaned up while a stolen lock is never disturbed.
func (r *ProxyRepository) CooldownDestination(ctx context.Context, id int64, owner string, lockTTL time.Duration, delay time.Duration) error {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE proxies
		SET destination_cooldown_until = GREATEST(
				COALESCE(destination_cooldown_until, '-infinity'::timestamptz),
				statement_timestamp() + ($2 * interval '1 second')),
			destination_failures = destination_failures + 1,
			locked_by = NULL,
			locked_at = NULL,
			updated_at = statement_timestamp()
		WHERE id = $1 AND locked_by = $3`,
		id, delay.Seconds(), owner)
	if err != nil {
		return fmt.Errorf("destination cooldown: %w", err)
	}
	return nil
}

// shouldRetire applies the retirement rule to a row after a conclusive
// failure: it never worked, has failed at least MinFailures times and was
// first seen at least MinAge ago. The SQL sweep (RetireStale) states the same
// predicate; keep them in step.
func shouldRetire(current *contract.ProxyRecord, newFailCount int, dbNow time.Time, rule contract.RetirementRule) bool {
	return rule.Enabled() &&
		current.LastAliveAt == nil &&
		newFailCount >= rule.MinFailures &&
		dbNow.Sub(current.FirstSeenAt) >= rule.MinAge
}

// RetireStale retires matching proxies in one bounded step. The step walks a
// window of at most batch rows in ID order starting after afterID (the window
// bounds the work whatever share of it matches) and retires the matching rows
// of that window in one short statement. Rows stay in the table, so the unique
// key keeps discovery from re-inserting them. The predicate mirrors
// shouldRetire; retired_at IS NULL makes repeats and concurrent sweeps no-ops.
func (r *ProxyRepository) RetireStale(ctx context.Context, rule contract.RetirementRule, afterID int64, batch int) (contract.RetireBatch, error) {
	if batch <= 0 {
		return contract.RetireBatch{}, fmt.Errorf("proxy retire: batch must be positive, got %d", batch)
	}
	if !rule.Enabled() {
		return contract.RetireBatch{NextID: afterID, Done: true}, nil
	}
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return contract.RetireBatch{}, err
	}
	var next, retired, examined int64
	err = db.QueryRowContext(ctx, `WITH win AS (
			SELECT id FROM proxies WHERE id > $1 ORDER BY id LIMIT $2
		), upd AS (
			UPDATE proxies p
			SET retired_at = statement_timestamp(), updated_at = statement_timestamp()
			FROM win
			WHERE p.id = win.id
			  AND p.retired_at IS NULL
			  AND p.last_alive_at IS NULL
			  AND p.fail_count >= $3
			  AND p.first_seen_at <= statement_timestamp() - ($4 * interval '1 second')
			RETURNING p.id
		)
		SELECT COALESCE((SELECT max(id) FROM win), $1::bigint),
			(SELECT count(*) FROM upd),
			(SELECT count(*) FROM win)`,
		afterID, batch, rule.MinFailures, rule.MinAge.Seconds()).Scan(&next, &retired, &examined)
	if err != nil {
		return contract.RetireBatch{}, fmt.Errorf("proxy retire: %w", err)
	}
	return contract.RetireBatch{NextID: next, Retired: retired, Done: examined == 0}, nil
}

// ResetDestinationFailures zeroes the consecutive destination failure counter
// of an owned proxy after a success. The destination_failures > 0 predicate
// makes the steady state (counter already zero) a no-row update, so healthy
// proxies cost no write per job.
func (r *ProxyRepository) ResetDestinationFailures(ctx context.Context, id int64, owner string) error {
	db, err := r.driver.Acquire(ctx)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE proxies
		SET destination_failures = 0, updated_at = statement_timestamp()
		WHERE id = $1 AND locked_by = $2 AND destination_failures > 0`,
		id, owner)
	if err != nil {
		return fmt.Errorf("reset destination failures: %w", err)
	}
	return nil
}

// failureStatus classifies one conclusive failure with the historical
// thresholds: dead when the incremented fail count reaches the threshold or
// when the endpoint's last success (first-seen fallback) is older than
// DeadAfter, inactive otherwise.
func failureStatus(current *contract.ProxyRecord, dbNow time.Time, update contract.ScanUpdate) (string, int) {
	newFailCount := current.FailCount + 1
	lastAlive := current.LastAliveAt
	if lastAlive == nil {
		fallback := current.FirstSeenAt
		lastAlive = &fallback
	}
	isDead := newFailCount >= update.FailThreshold || dbNow.Sub(*lastAlive) > update.DeadAfter
	if isDead {
		return contract.ProxyStatusDead, newFailCount
	}
	return contract.ProxyStatusInactive, newFailCount
}
