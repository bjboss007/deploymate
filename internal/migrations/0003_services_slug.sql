-- +goose Up
-- Services get a URL-safe slug for routing, like apps and projects.
ALTER TABLE services ADD COLUMN slug TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE services DROP COLUMN slug;
