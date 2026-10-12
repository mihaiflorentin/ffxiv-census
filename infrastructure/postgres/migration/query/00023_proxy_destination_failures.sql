-- +goose Up
-- Consecutive destination failures drive the growing destination cooldown.
-- A plain integer column: it is read and written together with
-- destination_cooldown_until by the same single UPDATE, so tracking the streak
-- adds no extra statement, and it is not indexed so the write stays HOT.
ALTER TABLE proxies
 ADD COLUMN destination_failures integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE proxies DROP COLUMN IF EXISTS destination_failures;
