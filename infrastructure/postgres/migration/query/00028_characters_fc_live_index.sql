-- +goose NO TRANSACTION
-- +goose Up
-- Free Company rankings (#64): the nightly full refresh groups live Free
-- Company members by fc_id in one streaming index-only pass, reading the world
-- and latest achievement time from the index itself. The partial index holds
-- only live characters in a Free Company, so it is far smaller than the full
-- fc_id index it replaces. Every other fc_id lookup (the census API's
-- free_company_id filter and the achievement filters) already restricts to
-- live characters (deleted_at IS NULL), and fc_id = $n implies fc_id IS NOT
-- NULL, so they use the partial index too.
-- CONCURRENTLY cannot run in a transaction, hence NO TRANSACTION above.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_fc_live
    ON characters (fc_id) INCLUDE (world, latest_achievement_at)
    WHERE deleted_at IS NULL AND fc_id IS NOT NULL;

DROP INDEX CONCURRENTLY IF EXISTS idx_characters_fc;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_fc ON characters (fc_id);

DROP INDEX CONCURRENTLY IF EXISTS idx_characters_fc_live;
