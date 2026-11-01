-- +goose NO TRANSACTION
-- +goose Up
-- Census tiers (#84): publish-character splits its budget between public
-- characters (tier 1), private characters that changed within the active
-- window (tier 2) and every other private character (tier 3). Every character
-- and achievement write recomputes census_tier; last_change_at is when a
-- census last found the character changed (NULL: never seen to change).
-- Adding a nullable column and a column with a constant default are both
-- catalog-only changes.
ALTER TABLE characters ADD COLUMN IF NOT EXISTS last_change_at TIMESTAMPTZ;
ALTER TABLE characters ADD COLUMN IF NOT EXISTS census_tier SMALLINT NOT NULL DEFAULT 3;

-- Seed: public characters start in tier 1, and private characters with a job
-- at 91 or above (levelled since Dawntrail) start in tier 2. With no recorded
-- change, their next census keeps them in tier 2 only if it finds one.
UPDATE characters SET census_tier = 1
 WHERE deleted_at IS NULL AND achievements_private = 0 AND census_tier <> 1;
UPDATE characters SET census_tier = 2
 WHERE deleted_at IS NULL AND achievements_private = 1 AND max_job_level >= 91 AND census_tier = 3;

-- Each tier's oldest characters come straight off the index.
-- CONCURRENTLY cannot run in a transaction, hence NO TRANSACTION above.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_characters_tier_census
    ON characters (census_tier, last_census_at NULLS FIRST, id)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_characters_tier_census;
ALTER TABLE characters DROP COLUMN IF EXISTS census_tier;
ALTER TABLE characters DROP COLUMN IF EXISTS last_change_at;
