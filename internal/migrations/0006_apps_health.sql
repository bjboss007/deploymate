-- +goose Up
-- Health state, maintained by the monitor's probe loop (not by docker):
-- healthy | unhealthy | '' (no port / not running / not yet probed).
ALTER TABLE apps ADD COLUMN health TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE apps DROP COLUMN health;
