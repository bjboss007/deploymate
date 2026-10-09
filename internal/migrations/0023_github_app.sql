-- +goose Up
-- The GitHub App this DeployMate registered through "Connect GitHub". At most one
-- row: one app per instance. The private key, webhook secret and client secret are
-- encrypted with the instance key like every other secret.
CREATE TABLE github_app (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    app_id             INTEGER NOT NULL,
    slug               TEXT NOT NULL,
    name               TEXT NOT NULL,
    html_url           TEXT NOT NULL,
    owner_login        TEXT NOT NULL DEFAULT '',
    owner_type         TEXT NOT NULL DEFAULT '',
    client_id          TEXT NOT NULL DEFAULT '',
    client_secret_enc  TEXT NOT NULL DEFAULT '',
    webhook_secret_enc TEXT NOT NULL,
    pem_enc            TEXT NOT NULL,
    webhook_url        TEXT NOT NULL DEFAULT '',   -- what the app was created with ('' = inactive)
    created_at         TEXT NOT NULL
);

-- +goose Down
DROP TABLE github_app;
