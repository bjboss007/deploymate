-- +goose Up
-- Manual apps (P2): image + internal port. Git apps (P4) leave image empty
-- and fill git_source_id + build_type instead.
ALTER TABLE apps ADD COLUMN image TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN port INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE apps DROP COLUMN image;
ALTER TABLE apps DROP COLUMN port;
