-- +goose Up
-- Environment scoping: staging and production are app/service labels that
-- select which manifest overlay and which services apply. Existing rows
-- become production — zero behavior change for current deployments.
ALTER TABLE apps ADD COLUMN environment TEXT NOT NULL DEFAULT 'production';
ALTER TABLE services ADD COLUMN environment TEXT NOT NULL DEFAULT 'production';

-- +goose Down
ALTER TABLE apps DROP COLUMN environment;
ALTER TABLE services DROP COLUMN environment;
