-- +goose Up
CREATE TABLE alerts (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL DEFAULT '',
    channel    TEXT NOT NULL DEFAULT 'webhook', -- webhook | email | telegram (future)
    url_enc    TEXT NOT NULL,                   -- encrypted endpoint (XChaCha20 envelope)
    enabled    INTEGER NOT NULL DEFAULT 1,
    events     TEXT NOT NULL DEFAULT '[]',      -- JSON array of subscribed event names
    created_at TEXT NOT NULL
);

CREATE TABLE alert_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    alert_id      TEXT NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    event         TEXT NOT NULL,
    subject       TEXT NOT NULL,
    ts            TEXT NOT NULL,
    delivered     INTEGER NOT NULL DEFAULT 0,
    response_code INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_alert_events_alert ON alert_events(alert_id, id);

-- +goose Down
DROP TABLE alert_events;
DROP TABLE alerts;
