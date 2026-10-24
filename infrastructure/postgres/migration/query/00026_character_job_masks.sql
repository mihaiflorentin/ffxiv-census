-- +goose Up
-- Job bitmasks: which jobs a character has levelled and which it has at the
-- level cap. The bit position is the folded official job id; NULL means not
-- computed yet. Nullable columns with no default are metadata-only, so this
-- never rewrites characters. Existing rows are filled by the operator-run
-- `backfill job-masks` command, never here.
ALTER TABLE characters
    ADD COLUMN IF NOT EXISTS jobs_levelled_mask BIGINT,
    ADD COLUMN IF NOT EXISTS jobs_at_cap_mask BIGINT;

-- The backfill's resumable cursor: the last character ID it walked.
CREATE TABLE IF NOT EXISTS job_mask_backfill_state (
    singleton  BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    last_id    BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS job_mask_backfill_state;

ALTER TABLE characters
    DROP COLUMN IF EXISTS jobs_at_cap_mask,
    DROP COLUMN IF EXISTS jobs_levelled_mask;
