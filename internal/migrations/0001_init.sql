-- +goose Up
CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'owner',
    created_at    TEXT NOT NULL
);

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    csrf_token TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE projects (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL
);

CREATE TABLE git_sources (
    id                 TEXT PRIMARY KEY,
    provider           TEXT NOT NULL,           -- github | gitlab | gitea
    repo_url           TEXT NOT NULL,
    clone_method       TEXT NOT NULL DEFAULT 'deploy_key', -- deploy_key | pat
    private_key_enc    TEXT NOT NULL DEFAULT '',
    pat_enc            TEXT NOT NULL DEFAULT '',
    webhook_secret_enc TEXT NOT NULL DEFAULT '',
    default_branch     TEXT NOT NULL DEFAULT 'main',
    created_at         TEXT NOT NULL
);

CREATE TABLE apps (
    id                    TEXT PRIMARY KEY,
    project_id            TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name                  TEXT NOT NULL,
    slug                  TEXT NOT NULL UNIQUE,
    git_source_id         TEXT REFERENCES git_sources(id) ON DELETE SET NULL,
    build_type            TEXT NOT NULL DEFAULT 'dockerfile', -- dockerfile | nixpacks
    root_directory        TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'stopped',    -- stopped | running | failed
    current_deployment_id TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL
);

CREATE TABLE env_vars (
    id         TEXT PRIMARY KEY,
    app_id     TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value_enc  TEXT NOT NULL,
    is_secret  INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    UNIQUE(app_id, key)
);

CREATE TABLE deployments (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    commit_sha     TEXT NOT NULL DEFAULT '',
    commit_message TEXT NOT NULL DEFAULT '',
    kind           TEXT NOT NULL DEFAULT 'deploy', -- deploy | rollback
    status         TEXT NOT NULL DEFAULT 'queued', -- queued | building | running | failed
    image_tag      TEXT NOT NULL DEFAULT '',
    error          TEXT NOT NULL DEFAULT '',
    started_at     TEXT NOT NULL DEFAULT '',
    finished_at    TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL
);

CREATE TABLE build_logs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    seq           INTEGER NOT NULL,
    stream        TEXT NOT NULL DEFAULT 'stdout', -- stdout | stderr | system
    line          TEXT NOT NULL,
    ts            TEXT NOT NULL
);
CREATE INDEX idx_build_logs_deployment ON build_logs(deployment_id, seq);

CREATE TABLE images (
    id            TEXT PRIMARY KEY,
    app_id        TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    tag           TEXT NOT NULL,
    deployment_id TEXT NOT NULL DEFAULT '',
    size_bytes    INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL
);

CREATE TABLE domains (
    id             TEXT PRIMARY KEY,
    app_id         TEXT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    hostname       TEXT NOT NULL UNIQUE,
    is_primary     INTEGER NOT NULL DEFAULT 0,
    tls_status     TEXT NOT NULL DEFAULT 'pending', -- pending | active | failed
    cert_expires_at TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL
);

CREATE TABLE services (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type        TEXT NOT NULL, -- postgres | mysql | redis
    name        TEXT NOT NULL,
    image       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'stopped', -- stopped | running | failed
    volume_name TEXT NOT NULL DEFAULT '',
    port        INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL
);

CREATE TABLE service_credentials (
    id         TEXT PRIMARY KEY,
    service_id TEXT NOT NULL REFERENCES services(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value_enc  TEXT NOT NULL
);

CREATE TABLE metrics (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id    TEXT NOT NULL,
    ts        TEXT NOT NULL,
    cpu_pct   REAL NOT NULL,
    mem_bytes INTEGER NOT NULL,
    net_rx    INTEGER NOT NULL,
    net_tx    INTEGER NOT NULL
);
CREATE INDEX idx_metrics_app_ts ON metrics(app_id, ts);

CREATE TABLE uptime_checks (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    domain_id   TEXT NOT NULL,
    ts          TEXT NOT NULL,
    ok          INTEGER NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    latency_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_uptime_domain_ts ON uptime_checks(domain_id, ts);

-- +goose Down
DROP TABLE uptime_checks;
DROP TABLE metrics;
DROP TABLE service_credentials;
DROP TABLE services;
DROP TABLE domains;
DROP TABLE images;
DROP TABLE build_logs;
DROP TABLE deployments;
DROP TABLE env_vars;
DROP TABLE apps;
DROP TABLE git_sources;
DROP TABLE projects;
DROP TABLE sessions;
DROP TABLE users;
