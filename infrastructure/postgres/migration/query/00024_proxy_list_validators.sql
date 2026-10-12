-- +goose Up
-- HTTP validators (ETag, Last-Modified) of the proxy provider lists discovery
-- last read completely, so the next run can ask the server whether a list
-- changed (conditional GET) instead of downloading and re-checking it. One row
-- per list URL; a handful of rows, rewritten at most once per list per run.
CREATE TABLE IF NOT EXISTS proxy_list_validators (
    url           TEXT        PRIMARY KEY,
    etag          TEXT        NOT NULL DEFAULT '',
    last_modified TEXT        NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS proxy_list_validators;
