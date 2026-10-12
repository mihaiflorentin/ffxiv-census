-- +goose NO TRANSACTION
-- +goose Up
-- Incremental UI statistics refresh: the snapshot remembers the time its
-- counts were taken (watermark_at), and the active/max-level recount reads
-- max_job_level through a partial index. Most characters have no job rows
-- (max_job_level = 0), so the index stays small.
-- CONCURRENTLY cannot run in a transaction, hence NO TRANSACTION above.
ALTER TABLE ui_stats_snapshots ADD COLUMN IF NOT EXISTS watermark_at TIMESTAMPTZ;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_max_job_level
    ON characters (max_job_level)
    WHERE max_job_level > 0;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_max_job_level;

ALTER TABLE ui_stats_snapshots DROP COLUMN IF EXISTS watermark_at;
