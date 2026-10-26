-- +goose Up
-- Worker nodes: other machines (reached over SSH on a private network such as Tailscale) that can run
-- containers for this DeployMate. Capacity is what the node's Docker reported at the last check.
CREATE TABLE nodes (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    endpoint       TEXT NOT NULL,                 -- ssh://user@host[:port] or tcp://host:port
    state          TEXT NOT NULL DEFAULT 'offline', -- ready | draining | offline
    cpus           INTEGER NOT NULL DEFAULT 0,
    mem_bytes      INTEGER NOT NULL DEFAULT 0,
    os             TEXT NOT NULL DEFAULT '',
    arch           TEXT NOT NULL DEFAULT '',
    docker_version TEXT NOT NULL DEFAULT '',
    gpu_runtime    INTEGER NOT NULL DEFAULT 0,    -- the NVIDIA container runtime is installed
    last_seen_at   TEXT NOT NULL DEFAULT '',
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL
);

-- +goose Down
DROP TABLE nodes;
