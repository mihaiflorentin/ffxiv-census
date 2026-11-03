-- UI statistics snapshot schema v6: the world groups carry daily
-- new-character series over the current window instead of weekly ones, for
-- the world pages' daily chart. Readers keep accepting stored v2 to v5
-- snapshots, and the incremental refresh rebuilds fully when the stored
-- snapshot is older, so the first refresh after deploy writes v6.
-- Only a CHECK constraint changes on a one-row table: no table rewrite.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3, 4, 5, 6));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ui_stats_snapshots WHERE schema_version = 6;
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3, 4, 5));
-- +goose StatementEnd
