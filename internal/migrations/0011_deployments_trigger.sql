-- +goose Up
-- Deployments now remember HOW they were started: 'dashboard' (git button),
-- 'webhook' (push), 'manual' (image deploy), 'rollback', or 'resize'.
-- 'kind' still discriminates deploy vs rollback vs manual; trigger adds the
-- webhook-vs-dashboard distinction that kind alone cannot express. Legacy
-- rows default to '' — the UI falls back to kind for those.
ALTER TABLE deployments ADD COLUMN trigger TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE deployments DROP COLUMN trigger;
