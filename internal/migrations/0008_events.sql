-- +goose Up
-- Append-only event log: every meaningful transition in the platform,
-- for the per-app history timeline and future insights. Deployments keep
-- their own table (richer); events cover everything else.
CREATE TABLE events (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    ts        TEXT NOT NULL,
    app_id    TEXT NOT NULL DEFAULT '',    -- empty for platform-wide events
    kind      TEXT NOT NULL,               -- health_unhealthy, resource_update, app_started, ...
    data      TEXT NOT NULL DEFAULT ''     -- human-readable detail
);
CREATE INDEX idx_events_app_ts ON events(app_id, id);

-- +goose Down
DROP TABLE events;
