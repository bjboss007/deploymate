-- +goose Up
-- The private address at which a node's published ports are reached. Empty = the host in the endpoint. Set it when the
-- endpoint is an SSH alias or reaches the machine another way (a tunnel) than the private network the ports are on.
ALTER TABLE nodes ADD COLUMN address TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN address;
