-- +goose Up
-- apps.volumes: folders inside the app's containers that live on a persistent volume, one absolute path per line.
ALTER TABLE apps ADD COLUMN volumes TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE apps DROP COLUMN volumes;
