package store

import "time"

// Event is one append-only history entry.
type Event struct {
	ID    int64
	TS    string
	AppID string
	Kind  string
	Data  string
}

// Event kinds (subset; more can be added without migration).
const (
	EventAppStarted     = "app_started"
	EventAppStopped     = "app_stopped"
	EventAppDeleted     = "app_deleted"
	EventHealthUnhealthy = "health_unhealthy"
	EventHealthRecovered = "health_recovered"
	EventAppHealed       = "app_healed"
	// EventAppScaled records a replica-count change (docs/specs/app-replicas.md).
	EventAppScaled = "app_scaled"
	// EventHealthPathChanged records an edit of apps.health_path.
	EventHealthPathChanged = "health_path_changed"
	EventResourceUpdate  = "resource_update"
	EventResourceResized = "resource_resized"
	EventEnvChanged      = "env_changed"
	EventEnvRemoved      = "env_removed"
	EventRuntimeChanged  = "runtime_changed"
	EventRootDirChanged  = "root_directory_changed"
	// EventDeploySkipped: a push arrived but changed nothing in the app's build folder.
	EventDeploySkipped   = "deploy_skipped"
	// EventServerCleanup: the owner pruned build cache and unused images from the Server page.
	EventServerCleanup = "server_cleanup"
	EventGitHubConnected    = "github_connected"
	EventGitHubDisconnected = "github_disconnected"
	EventGitConnected    = "git_connected"
	EventAPIAction       = "api_action" // something changed through the API (agent/script); the data names the token
	EventServiceStarted  = "service_started"
	EventServiceStopped  = "service_stopped"
	EventAppRestarted    = "app_restarted"
	EventServiceRestarted = "service_restarted"
	// EventServiceAutoProvisioned: a deploy manifest created a service
	// without any human involvement.
	EventServiceAutoProvisioned = "service_auto_provisioned"
	// EventEnvironmentChanged: an app moved between staging/production.
	EventEnvironmentChanged = "environment_changed"
	// EventServiceOrphaned: a manifest deploy stopped declaring a service
	// it had created; it is flagged for a human to delete or keep.
	EventServiceOrphaned = "service_orphaned"
	// EventDNSRecordFailed: auto-DNS could not create the app's preview
	// record; the preview URL stays without an edge cert until fixed.
	EventDNSRecordFailed = "dns_record_failed"
	// Backup events (app_id empty — they hang off services): the events
	// timeline is the run history for backups and restores.
	EventBackupEnabled       = "backup_enabled"
	EventBackupDisabled      = "backup_disabled"
	EventBackupConfigChanged = "backup_config_changed"
	EventBackupOK            = "backup_ok"
	EventBackupFailed        = "backup_failed"
	// EventBackupSkipped: a scheduled window fired but the service's
	// container was not running (DeployMate never starts a service just to
	// back it up). The window still counts as handled.
	EventBackupSkipped = "backup_skipped"
	// EventBackupPruneFailed: retention pruning hit an error; the fresh
	// backup itself succeeded (prune failures are warnings, never failures
	// of the run).
	EventBackupPruneFailed = "backup_prune_failed"
	EventRestoreStarted    = "restore_started"
	EventRestoreOK         = "restore_ok"
	EventRestoreFailed     = "restore_failed"
)

// RecordEvent appends one event.
func (s *Store) RecordEvent(appID, kind, data string) error {
	_, err := s.db.Exec(
		`INSERT INTO events (ts, app_id, kind, data) VALUES (?, ?, ?, ?)`,
		Now(), appID, kind, data,
	)
	return err
}

// ListEvents returns the newest events for an app, newest first.
func (s *Store) ListEvents(appID string, limit int) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, ts, app_id, kind, data FROM events WHERE app_id = ? ORDER BY id DESC LIMIT ?`,
		appID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.AppID, &e.Kind, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeploymentStats aggregates deploy behavior over a window.
type DeploymentStats struct {
	Total     int
	Succeeded int
	AvgBuildSec float64 // over deployments with both timestamps
}

// DeploymentStatsFor computes totals for an app since `since`.
func (s *Store) DeploymentStatsFor(appID string, since time.Time) (DeploymentStats, error) {
	var out DeploymentStats
	sinceS := since.UTC().Format(time.RFC3339Nano)
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0)
		 FROM deployments WHERE app_id = ? AND created_at >= ? AND kind IN ('deploy', 'manual', 'rollback', 'resize')`,
		appID, sinceS,
	).Scan(&out.Total, &out.Succeeded); err != nil {
		return out, err
	}
	var sum, count float64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(julianday(finished_at) - julianday(started_at)), 0), COUNT(*)
		 FROM deployments WHERE app_id = ? AND created_at >= ? AND started_at != '' AND finished_at != ''`,
		appID, sinceS,
	).Scan(&sum, &count); err != nil {
		return out, err
	}
	if count > 0 {
		out.AvgBuildSec = sum * 86400 / count
	}
	return out, nil
}

// DeploymentStatsAll computes fleet-wide deploy behavior over a window
// (the app-scoped DeploymentStatsFor minus the app filter).
func (s *Store) DeploymentStatsAll(since time.Time) (DeploymentStats, error) {
	var out DeploymentStats
	sinceS := since.UTC().Format(time.RFC3339Nano)
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0)
		 FROM deployments WHERE created_at >= ? AND kind IN ('deploy', 'manual', 'rollback', 'resize')`,
		sinceS,
	).Scan(&out.Total, &out.Succeeded); err != nil {
		return out, err
	}
	var sum, count float64
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(julianday(finished_at) - julianday(started_at)), 0), COUNT(*)
		 FROM deployments WHERE created_at >= ? AND started_at != '' AND finished_at != ''`,
		sinceS,
	).Scan(&sum, &count); err != nil {
		return out, err
	}
	if count > 0 {
		out.AvgBuildSec = sum * 86400 / count
	}
	return out, nil
}

// AppDeploymentStats is one app's row in the fleet stats table.
type AppDeploymentStats struct {
	Name        string
	Slug        string
	Total       int
	Succeeded   int
	AvgBuildSec float64
}

// DeploymentStatsPerApp computes per-app deploy behavior over a window,
// most-deployed first. Apps with no deployments in the window are absent.
func (s *Store) DeploymentStatsPerApp(since time.Time) ([]AppDeploymentStats, error) {
	sinceS := since.UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(
		`SELECT a.name, a.slug,
		        COUNT(d.id),
		        COALESCE(SUM(CASE WHEN d.status = 'running' THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(julianday(d.finished_at) - julianday(d.started_at)), 0) * 86400,
		        COUNT(CASE WHEN d.started_at != '' AND d.finished_at != '' THEN 1 END)
		 FROM deployments d JOIN apps a ON a.id = d.app_id
		 WHERE d.created_at >= ? AND d.kind IN ('deploy', 'manual', 'rollback', 'resize')
		 GROUP BY a.id ORDER BY COUNT(d.id) DESC, a.name`,
		sinceS,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppDeploymentStats
	for rows.Next() {
		var a AppDeploymentStats
		var sum, timed float64
		if err := rows.Scan(&a.Name, &a.Slug, &a.Total, &a.Succeeded, &sum, &timed); err != nil {
			return nil, err
		}
		if timed > 0 {
			a.AvgBuildSec = sum / timed
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DayCount is one day's deploy count for the fleet chart.
type DayCount struct {
	Day   string // YYYY-MM-DD (UTC)
	Count int
}

// DeploysPerDay buckets deploy behavior by UTC day over the window, oldest
// day first. Days without deploys are absent (the chart connects gaps).
func (s *Store) DeploysPerDay(since time.Time) ([]DayCount, error) {
	sinceS := since.UTC().Format(time.RFC3339Nano)
	rows, err := s.db.Query(
		`SELECT date(created_at), COUNT(*)
		 FROM deployments WHERE created_at >= ? AND kind IN ('deploy', 'manual', 'rollback', 'resize')
		 GROUP BY date(created_at) ORDER BY date(created_at)`,
		sinceS,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayCount
	for rows.Next() {
		var dc DayCount
		if err := rows.Scan(&dc.Day, &dc.Count); err != nil {
			return nil, err
		}
		out = append(out, dc)
	}
	return out, rows.Err()
}

// UptimePercent returns the fraction of successful probes over the window
// for the given domain.
func (s *Store) UptimePercent(domainID string, since time.Time) (float64, int, error) {
	sinceS := since.UTC().Format(time.RFC3339Nano)
	var total, okCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(ok), 0) FROM uptime_checks WHERE domain_id = ? AND ts >= ?`,
		domainID, sinceS,
	).Scan(&total, &okCount); err != nil {
		return 0, 0, err
	}
	if total == 0 {
		return 0, 0, nil
	}
	return float64(okCount) / float64(total) * 100, total, nil
}

// MTTRSeconds computes mean time-to-recovery from health_unhealthy →
// health_recovered event pairs. Returns 0 when there are no pairs.
func (s *Store) MTTRSeconds(appID string) (float64, int, error) {
	rows, err := s.db.Query(
		`SELECT kind, ts FROM events
		 WHERE app_id = ? AND kind IN ('health_unhealthy', 'health_recovered')
		 ORDER BY id`,
		appID,
	)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var open time.Time
	var sum float64
	var pairs int
	for rows.Next() {
		var kind, tsS string
		if err := rows.Scan(&kind, &tsS); err != nil {
			return 0, 0, err
		}
		ts, err := time.Parse(time.RFC3339Nano, tsS)
		if err != nil {
			continue
		}
		if kind == EventHealthUnhealthy && open.IsZero() {
			open = ts
		} else if kind == EventHealthRecovered && !open.IsZero() {
			sum += ts.Sub(open).Seconds()
			pairs++
			open = time.Time{}
		}
	}
	if pairs == 0 {
		return 0, 0, nil
	}
	return sum / float64(pairs), pairs, nil
}
