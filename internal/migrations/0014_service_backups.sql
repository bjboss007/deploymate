-- +goose Up
-- Per-service database backup configuration (docs/specs/database-backups.md).
-- 1:1 with services: opt-in per service; the schedule is 5-field cron in
-- server-local time; keep prunes to the newest N objects; destination picks
-- one of the server's DEPLOYMATE_BACKUP_DEST_* blocks; key_enc holds the
-- service's own backup key (32 bytes), XChaCha20'd with the server key and
-- downloadble once per need; last_run_at persists when the last scheduled
-- window was HANDLED (ran, failed, or skipped) so a restart can never
-- double-fire a window.
CREATE TABLE service_backups (
    service_id  TEXT PRIMARY KEY REFERENCES services(id) ON DELETE CASCADE,
    enabled     INTEGER NOT NULL DEFAULT 0,
    schedule    TEXT NOT NULL DEFAULT '0 2 * * *',
    keep        INTEGER NOT NULL DEFAULT 14,
    destination TEXT NOT NULL DEFAULT 'default',
    key_enc     TEXT NOT NULL DEFAULT '',
    last_run_at TEXT NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE service_backups;
