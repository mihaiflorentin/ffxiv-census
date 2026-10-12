-- When an achievement census last found a character private, so the time its
-- achievements_private flag was last verified. NULL until then. A nullable
-- column without a default is a catalog-only change: no table rewrite.
-- +goose Up
ALTER TABLE characters ADD COLUMN achievements_checked_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE characters DROP COLUMN IF EXISTS achievements_checked_at;
