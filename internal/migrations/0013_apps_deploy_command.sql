-- +goose Up
-- Manual image deploys may override the container's entrypoint and command
-- (one-off jobs, images with odd entrypoints). Both columns hold the raw
-- whitespace-separated string from the deploy form; appspec.SplitArgs turns
-- it into argv at spec-build time, and an empty string means "image
-- default" (the fields are simply not set on container.Config). Set from
-- the same deploy form as image/port, so restart/heal/rollback inherit
-- them from the app row.
ALTER TABLE apps ADD COLUMN entrypoint TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN command TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE apps DROP COLUMN command;
ALTER TABLE apps DROP COLUMN entrypoint;
