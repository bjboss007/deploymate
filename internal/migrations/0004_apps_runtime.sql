-- +goose Up
-- Runtime selection: empty = Dockerfile build (default); otherwise a
-- railpack provider spec like "node:22" or "python" — builds happen
-- without a Dockerfile, with the runtime installed by mise.
ALTER TABLE apps ADD COLUMN runtime TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE apps DROP COLUMN runtime;
