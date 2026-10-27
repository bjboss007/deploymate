-- +goose Up
-- apps.gpu: 1 = the app's containers get every GPU on the machine they run on (docker --gpus all).
-- nodes.gpu_info: what the node's GPUs are, as one line ("NVIDIA GeForce RTX 4090 24 GB"); '' = none or unknown.
ALTER TABLE apps ADD COLUMN gpu INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN gpu_info TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN gpu_info;
ALTER TABLE apps DROP COLUMN gpu;
