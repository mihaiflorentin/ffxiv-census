-- +goose Up
-- Item level and gear were never filled: the profile parser never read an
-- item level (characters.item_level was 0 everywhere) and nothing wrote
-- character_gear (no rows). v1.23.0 stopped reading and writing both.
-- Both drops are catalog-only but need an exclusive lock on characters,
-- which census writes hold briefly all the time: give up after 10s rather
-- than queue every write behind the lock, and let the migration retry.
-- +goose StatementBegin
SET LOCAL lock_timeout = '10s';
DROP TABLE IF EXISTS character_gear;
ALTER TABLE characters DROP COLUMN IF EXISTS item_level;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE characters ADD COLUMN IF NOT EXISTS item_level INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS character_gear (
    character_id BIGINT       NOT NULL,
    slot         VARCHAR(50)  NOT NULL,
    item_id      INTEGER      NOT NULL,
    name         VARCHAR(255) NOT NULL,
    item_level   INTEGER      NOT NULL DEFAULT 0,
    dye          VARCHAR(100),
    materia      TEXT,
    updated_at   TIMESTAMPTZ  NOT NULL,
    PRIMARY KEY (character_id, slot),
    FOREIGN KEY (character_id) REFERENCES characters(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_character_gear_char_id ON character_gear(character_id);
-- +goose StatementEnd
