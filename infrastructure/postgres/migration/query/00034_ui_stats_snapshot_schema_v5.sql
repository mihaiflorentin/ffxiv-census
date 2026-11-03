-- UI statistics snapshot schema v5 (#94): the payload adds the created
-- characters, dated by character ID, beside the company chocobo figures.
-- Readers keep accepting stored v2 to v4 snapshots, and the incremental
-- refresh rebuilds fully when the stored snapshot is older, so the first
-- refresh after deploy writes v5.
-- Only a CHECK constraint changes on a one-row table: no table rewrite.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3, 4, 5));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ui_stats_snapshots WHERE schema_version = 5;
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_schema_version_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_schema_version_check CHECK (schema_version IN (1, 2, 3, 4));
-- +goose StatementEnd
