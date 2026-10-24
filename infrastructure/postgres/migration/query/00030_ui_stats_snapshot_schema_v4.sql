-- UI statistics snapshot schema v4 (spec #66): the payload records the
-- new-characters window, keeps the daily new-character series at whole-game
-- scope over two windows, and stores per-world window totals on the world
-- groups. Readers keep accepting stored v2 and v3 snapshots, and the
-- incremental refresh rebuilds fully when the stored snapshot is older, so
-- the first refresh after deploy writes v4.
-- Only a CHECK constraint changes on a one-row table: no table rewrite.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3, 4));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ui_stats_snapshots WHERE schema_version = 4;
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3));
-- +goose StatementEnd
