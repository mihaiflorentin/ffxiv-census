-- +goose NO TRANSACTION
-- +goose Up
-- Records the two recovery-queue indexes that were created by hand on
-- production on 2026-10-12 (IF NOT EXISTS: they already exist there) and drops
-- the two legacy indexes no query uses. Both legacy indexes covered columns
-- (status, locked_at, last_scanned_at) that the consumer path rewrites on every
-- claim/refresh/release, which made those updates non-HOT.
-- CONCURRENTLY cannot run in a transaction, hence NO TRANSACTION above.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxies_recovery_claim
    ON proxies (next_attempt_at NULLS FIRST, id)
    INCLUDE (last_verified_at, scan_lease_until, scan_not_before)
    WHERE NOT general_healthy AND last_verified_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxies_recovery_recent
    ON proxies (last_verified_at)
    WHERE NOT general_healthy AND last_verified_at IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS idx_proxies_available;

DROP INDEX CONCURRENTLY IF EXISTS idx_proxies_scan_priority;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxies_scan_priority
    ON proxies (status, last_scanned_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxies_available
    ON proxies (status, locked_at, latency_ms)
    WHERE status = 'active';

DROP INDEX CONCURRENTLY IF EXISTS idx_proxies_recovery_recent;

DROP INDEX CONCURRENTLY IF EXISTS idx_proxies_recovery_claim;
