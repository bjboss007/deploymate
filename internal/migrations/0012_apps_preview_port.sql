-- +goose Up
-- Apps remember the loopback host port their CURRENT container publishes
-- (zero-downtime deploys start the new container on a fresh temp port, probe
-- it, then record the port before removing the old one). 0 = fall back to
-- the deterministic crc32 hash (runtime.PreviewPort). Legacy rows keep the
-- hash behavior.
ALTER TABLE apps ADD COLUMN preview_host_port INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE apps DROP COLUMN preview_host_port;
