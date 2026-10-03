-- +goose Up
-- How an app is drawn on its card (docs: UI redesign, app resource cards).
-- logo:   an owner-chosen technology logo key ('' = automatic)
-- accent: an owner-chosen identity colour key ('' = derived from the name)
-- stack:  the framework detected at deploy time ('' = unknown): "react",
--         "spring", "django", ... — a best guess, overridden by logo.
ALTER TABLE apps ADD COLUMN logo TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN accent TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN stack TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE apps DROP COLUMN stack;
ALTER TABLE apps DROP COLUMN accent;
ALTER TABLE apps DROP COLUMN logo;
