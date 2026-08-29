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
	EventResourceUpdate  = "resource_update"
	EventResourceResized = "resource_resized"
	EventEnvChanged      = "env_changed"
	EventEnvRemoved      = "env_removed"
	EventRuntimeChanged  = "runtime_changed"
	EventGitConnected    = "git_connected"
	EventServiceStarted  = "service_started"
	EventServiceStopped  = "service_stopped"
	EventAppRestarted    = "app_restarted"
	EventServiceRestarted = "service_restarted"
	// EventServiceAutoProvisioned: a deploy manifest created a service
	// without any human involvement.
	EventServiceAutoProvisioned = "service_auto_provisioned"
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
		 FROM deployments WHERE app_id = ? AND created_at >= ? AND kind IN ('deploy', 'rollback', 'resize')`,
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
