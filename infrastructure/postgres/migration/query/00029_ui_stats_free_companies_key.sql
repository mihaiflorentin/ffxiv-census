-- Free Company rankings (#64): the boards live in their own snapshot row,
-- 'free_companies', next to 'current', so the main snapshot reloaded every
-- minute stays small. Only a CHECK constraint changes: no table rewrite.
-- +goose Up
-- +goose StatementBegin
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_snapshot_key_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_snapshot_key_check CHECK (snapshot_key IN ('current', 'free_companies'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM ui_stats_snapshots WHERE snapshot_key = 'free_companies';
ALTER TABLE ui_stats_snapshots DROP CONSTRAINT ui_stats_snapshots_snapshot_key_check;
ALTER TABLE ui_stats_snapshots ADD CONSTRAINT ui_stats_snapshots_snapshot_key_check CHECK (snapshot_key = 'current');
-- +goose StatementEnd
