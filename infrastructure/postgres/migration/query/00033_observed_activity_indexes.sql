-- +goose NO TRANSACTION
-- +goose Up
-- Observed activity (#90): a character is active when its latest public
-- achievement or its last observed change (last_change_at) is in the
-- activity window.
--
-- The incremental UI stats pass and CountActive find recently changed
-- characters through this partial index, which holds only live characters
-- seen to change, far fewer than all characters.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_last_change
    ON characters (last_change_at)
    WHERE deleted_at IS NULL AND last_change_at IS NOT NULL;

-- The Free Company boards' grouped pass stays index-only: the members index
-- now also carries last_change_at. The new index is built under a temporary
-- name and swapped in, so the boards always have one to read.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_fc_live_v2
    ON characters (fc_id) INCLUDE (world, latest_achievement_at, last_change_at)
    WHERE deleted_at IS NULL AND fc_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_fc_live;
ALTER INDEX IF EXISTS idx_characters_fc_live_v2 RENAME TO idx_characters_fc_live;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_fc_live_v1
    ON characters (fc_id) INCLUDE (world, latest_achievement_at)
    WHERE deleted_at IS NULL AND fc_id IS NOT NULL;
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_fc_live;
ALTER INDEX IF EXISTS idx_characters_fc_live_v1 RENAME TO idx_characters_fc_live;
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_last_change;
