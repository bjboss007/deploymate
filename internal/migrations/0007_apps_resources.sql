-- +goose Up
-- Resource limits, computed by the monitor from observed usage (P90 with
-- headroom) and applied as docker limits at the next deploy.
-- 0 = not yet detected (no metrics) → unlimited until the app has run.
ALTER TABLE apps ADD COLUMN mem_limit_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN cpu_limit REAL NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE apps DROP COLUMN mem_limit_mb;
ALTER TABLE apps DROP COLUMN cpu_limit;
