-- +goose Up
-- A project can be pinned to a worker node: its apps and services then run together on that machine (container names
-- only resolve on one machine's network, so they cannot be split). '' = this server.
ALTER TABLE projects ADD COLUMN node_id TEXT NOT NULL DEFAULT '';

-- Which node each container lives on, so later calls by name find it. A container with no row is on this server.
CREATE TABLE container_nodes (
    name    TEXT PRIMARY KEY,
    node_id TEXT NOT NULL
);

-- +goose Down
DROP TABLE container_nodes;
ALTER TABLE projects DROP COLUMN node_id;
