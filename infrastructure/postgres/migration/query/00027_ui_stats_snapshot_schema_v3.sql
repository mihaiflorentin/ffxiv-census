-- UI statistics snapshot schema v3 (spec #56): the payload gains the Grand
-- Company dimensions and later the dashboard sections. Readers keep accepting
-- stored v2 snapshots, and the incremental refresh rebuilds fully when the
-- stored snapshot is older, so the first refresh after deploy writes v3.
-- Only a CHECK constraint changes: no table rewrite.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ui_stats_snapshots WHERE schema_version = 3;
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2));
-- +goose StatementEnd
