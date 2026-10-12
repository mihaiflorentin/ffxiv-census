-- +goose NO TRANSACTION

-- CREATE INDEX CONCURRENTLY cannot run inside a transaction block, hence the
-- directive above; goose then runs each statement on its own. The build does
-- not block writes to characters. If a concurrent build is interrupted it
-- leaves an INVALID index behind: drop it and re-run the migration.

-- +goose Up
-- Serves the character-census publisher's oldest-first selection of live
-- characters: the index order matches ORDER BY last_census_at ASC NULLS FIRST,
-- id, so LIMIT n reads n index entries instead of sorting the table.
-- +goose StatementBegin
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_live_census
    ON characters (last_census_at NULLS FIRST, id)
    WHERE deleted_at IS NULL;
-- +goose StatementEnd

-- Serves the deleted-character re-probe selection (longest-deleted first).
-- Only deleted rows are indexed, a small fraction of the table.
-- +goose StatementBegin
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_deleted_at
    ON characters (deleted_at, id)
    WHERE deleted_at IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_deleted_at;
-- +goose StatementEnd
-- +goose StatementBegin
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_live_census;
-- +goose StatementEnd
