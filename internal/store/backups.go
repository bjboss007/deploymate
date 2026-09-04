package store

import (
	"database/sql"
	"errors"
)

// Default schedule / retention for a newly enabled backup (spec defaults:
// daily 02:00 server-local, keep the newest 14 objects).
const (
	DefaultBackupSchedule = "0 2 * * *"
	DefaultBackupKeep     = 14
)

// BackupConfig is a service's backup opt-in row (service_backups, 1:1 with
// services). KeyEnc and LastRunAt are opaque strings — the caller encrypts
// and formats.
type BackupConfig struct {
	ServiceID   string
	Enabled     bool
	Schedule    string
	Keep        int
	Destination string
	KeyEnc      string
	LastRunAt   string
}

// BackupTarget joins a service to its (enabled) backup config — what the
// scheduler iterates.
type BackupTarget struct {
	BackupConfig
	Service Service
}

// GetBackupConfig fetches a service's backup row. A service that was never
// opted in has no row: ErrNotFound.
func (s *Store) GetBackupConfig(serviceID string) (BackupConfig, error) {
	var c BackupConfig
	var enabled int
	err := s.db.QueryRow(
		`SELECT service_id, enabled, schedule, keep, destination, key_enc, last_run_at
		 FROM service_backups WHERE service_id = ?`,
		serviceID,
	).Scan(&c.ServiceID, &enabled, &c.Schedule, &c.Keep, &c.Destination, &c.KeyEnc, &c.LastRunAt)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	c.Enabled = enabled != 0
	return c, err
}

// SaveBackupConfig upserts a service's backup row (opt-in, settings changes,
// and key generation all land here). Zero fields take the spec defaults (the
// SQL column defaults only apply when a column is omitted, and every INSERT
// here names them all). last_run_at is deliberately NOT overwritten on
// conflict — SetBackupLastRun is its only writer, so a settings save can
// never reset the scheduler's window bookkeeping.
func (s *Store) SaveBackupConfig(c BackupConfig) error {
	if c.Schedule == "" {
		c.Schedule = DefaultBackupSchedule
	}
	if c.Keep == 0 {
		c.Keep = DefaultBackupKeep
	}
	if c.Destination == "" {
		c.Destination = "default"
	}
	_, err := s.db.Exec(
		`INSERT INTO service_backups (service_id, enabled, schedule, keep, destination, key_enc, last_run_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(service_id) DO UPDATE SET
		   enabled = excluded.enabled, schedule = excluded.schedule, keep = excluded.keep,
		   destination = excluded.destination, key_enc = excluded.key_enc`,
		c.ServiceID, c.Enabled, c.Schedule, c.Keep, c.Destination, c.KeyEnc, c.LastRunAt,
	)
	return err
}

// SetBackupLastRun records that a scheduled window was handled (ran, failed,
// or skipped-because-stopped). The scheduler's next() computes from this, so
// persisting it is what makes a restart unable to double-fire a window.
func (s *Store) SetBackupLastRun(serviceID, ts string) error {
	_, err := s.db.Exec(`UPDATE service_backups SET last_run_at = ? WHERE service_id = ?`, ts, serviceID)
	return err
}

// ListEnabledBackupTargets returns every opted-in service with its config,
// for the scheduler's tick.
func (s *Store) ListEnabledBackupTargets() ([]BackupTarget, error) {
	rows, err := s.db.Query(
		`SELECT b.service_id, b.enabled, b.schedule, b.keep, b.destination, b.key_enc, b.last_run_at,
		        sv.id, sv.project_id, sv.type, sv.name, sv.slug, sv.image, sv.status, sv.volume_name,
		        sv.port, sv.environment, sv.origin, sv.orphaned, sv.created_at
		 FROM service_backups b JOIN services sv ON sv.id = b.service_id
		 WHERE b.enabled = 1 ORDER BY sv.name`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BackupTarget
	for rows.Next() {
		var t BackupTarget
		var enabled int
		if err := rows.Scan(&t.ServiceID, &enabled, &t.Schedule, &t.Keep, &t.Destination, &t.KeyEnc, &t.LastRunAt,
			&t.Service.ID, &t.Service.ProjectID, &t.Service.Type, &t.Service.Name, &t.Service.Slug,
			&t.Service.Image, &t.Service.Status, &t.Service.VolumeName, &t.Service.Port,
			&t.Service.Environment, &t.Service.Origin, &t.Service.Orphaned, &t.Service.CreatedAt); err != nil {
			return nil, err
		}
		t.Enabled = enabled != 0
		out = append(out, t)
	}
	return out, rows.Err()
}
