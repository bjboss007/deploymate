-- +goose Up
-- One reading of the host every minute (CPU, memory, disk, load, network,
-- temperature) for the Server page's history charts. Kept 7 days.
CREATE TABLE host_metrics (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ts       TEXT NOT NULL,
    cpu_pct  REAL NOT NULL,
    mem_pct  REAL NOT NULL,
    disk_pct REAL NOT NULL,
    load1    REAL NOT NULL,
    net_rx   INTEGER NOT NULL,   -- bytes per second
    net_tx   INTEGER NOT NULL,
    temp_c   REAL NOT NULL DEFAULT -1  -- -1 = no sensor
);
CREATE INDEX host_metrics_ts ON host_metrics(ts);

-- +goose Down
DROP TABLE host_metrics;
