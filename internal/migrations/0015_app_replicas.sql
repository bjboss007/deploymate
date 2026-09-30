-- +goose Up
-- App replicas (docs/specs/app-replicas.md): an app runs N identical
-- containers ("slots") behind one address. apps.replicas is the desired
-- count (1..5, capped in code); apps.health_path feeds both the monitor's
-- per-slot probe and the Traefik active healthcheck, so they agree on what
-- "serving" means.
ALTER TABLE apps ADD COLUMN replicas INTEGER NOT NULL DEFAULT 1;
ALTER TABLE apps ADD COLUMN health_path TEXT NOT NULL DEFAULT '/';

-- One row per slot a swap actually recorded: the container name, the
-- loopback host port it publishes, the monitor's last per-slot verdict
-- (healthy | unhealthy | ''), and the deployment whose image it runs (drift
-- is visible when slots disagree). Slot 1 is the canonical dm-{slug}
-- container; apps.preview_host_port is still dual-written with slot 1's
-- port for one release so a binary rollback can resolve previews.
CREATE TABLE app_replicas (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    slot           INTEGER NOT NULL,
    container_name TEXT NOT NULL,
    host_port      INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT '',
    deploy_id      TEXT NOT NULL DEFAULT '',
    updated_at     TEXT NOT NULL,
    UNIQUE (app_id, slot)
);

-- +goose Down
DROP TABLE app_replicas;
ALTER TABLE apps DROP COLUMN health_path;
ALTER TABLE apps DROP COLUMN replicas;
