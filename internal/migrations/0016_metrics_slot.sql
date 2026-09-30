-- +goose Up
-- Per-replica metrics (replicas follow-up, ADR 0018): each sample records
-- the replica slot it came from. Pre-replicas samples were all slot 1 (the
-- canonical dm-{slug} container), which the default states.
ALTER TABLE metrics ADD COLUMN slot INTEGER NOT NULL DEFAULT 1;
CREATE INDEX idx_metrics_app_slot_ts ON metrics(app_id, slot, ts);

-- +goose Down
DROP INDEX idx_metrics_app_slot_ts;
ALTER TABLE metrics DROP COLUMN slot;
