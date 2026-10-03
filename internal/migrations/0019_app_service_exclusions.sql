-- +goose Up
-- By default every app receives the connection URL of every service in its
-- project and environment. A row here says "not this one": the app no longer
-- gets that service's URL (a frontend that never touches the database, say).
CREATE TABLE app_service_exclusions (
    app_id     TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (app_id, service_id)
);

-- +goose Down
DROP TABLE app_service_exclusions;
