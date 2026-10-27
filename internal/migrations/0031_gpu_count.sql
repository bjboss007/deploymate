-- +goose Up
-- apps.gpu_count: how many GPUs an app with gpu = 1 gets; 0 = every GPU on the machine.
ALTER TABLE apps ADD COLUMN gpu_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE apps DROP COLUMN gpu_count;
