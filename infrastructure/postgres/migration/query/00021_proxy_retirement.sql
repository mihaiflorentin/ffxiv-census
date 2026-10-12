-- +goose NO TRANSACTION
-- +goose Up
-- Retired proxies: rows that never worked and keep failing leave the
-- background scan but stay in the table, so the unique key (protocol, ip, port)
-- keeps discovery from re-inserting them. retired_at is the marker (NULL means
-- live). last_listed_at records when a provider last listed the endpoint, for
-- revival and for the once-per-hour write limit.
--
-- Both columns are nullable with no default, so ADD COLUMN is a catalog-only
-- change and does not rewrite the table. Existing rows are NOT retired here: a
-- single UPDATE over ~1.85M rows would hold a long write transaction on every
-- booting replica. The scan command retires them in paced batches instead
-- (see domain/proxy/worker/retire.go).
ALTER TABLE proxies ADD COLUMN IF NOT EXISTS retired_at timestamptz;

ALTER TABLE proxies ADD COLUMN IF NOT EXISTS last_listed_at timestamptz;

-- The live background candidates, ordered exactly like the background claim.
-- CONCURRENTLY keeps writes flowing during the build and cannot run in a
-- transaction, hence NO TRANSACTION above. The older
-- idx_proxies_background_completed stays until every replica runs the
-- retirement-aware claim, then a follow-up migration can drop it.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_proxies_background_live
    ON proxies (last_completed_at NULLS FIRST, id)
    WHERE NOT general_healthy AND retired_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_proxies_background_live;

ALTER TABLE proxies DROP COLUMN IF EXISTS last_listed_at;

ALTER TABLE proxies DROP COLUMN IF EXISTS retired_at;
