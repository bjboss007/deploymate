package store

import (
	"database/sql"
	"errors"
)

// MaxReplicas caps an app's slot count: a typo must never spawn 100
// containers on a single node (docs/specs/app-replicas.md, decision 3).
const MaxReplicas = 5

// DefaultHealthPath is the per-slot probe path for apps that never set one.
const DefaultHealthPath = "/"

// ErrReplicasOutOfRange rejects a replica count outside 1..MaxReplicas.
var ErrReplicasOutOfRange = errors.New("store: replicas must be between 1 and 5")

// ClampReplicas maps any stored/legacy value into 1..MaxReplicas.
func ClampReplicas(n int) int {
	if n < 1 {
		return 1
	}
	if n > MaxReplicas {
		return MaxReplicas
	}
	return n
}

// AppReplica is one running slot of an app: what a swap recorded (the
// container name and the loopback host port it publishes), the monitor's
// last per-slot verdict, and the deployment whose image it runs.
type AppReplica struct {
	ID            string
	AppID         string
	Slot          int
	ContainerName string
	HostPort      int
	Status        string // healthy | unhealthy | '' (not yet probed)
	DeployID      string
	UpdatedAt     string
}

// UpdateAppReplicas sets the desired slot count, rejecting out-of-range
// values rather than clamping — the form must say no, not silently change
// the operator's number.
func (s *Store) UpdateAppReplicas(id string, n int) error {
	if n < 1 || n > MaxReplicas {
		return ErrReplicasOutOfRange
	}
	_, err := s.db.Exec(`UPDATE apps SET replicas = ? WHERE id = ?`, n, id)
	return err
}

// UpdateAppHealthPath sets the path the monitor probes every replica at
// and the Traefik healthcheck uses (the latter from the next deploy — docker
// labels are immutable). The caller validates the path.
func (s *Store) UpdateAppHealthPath(id, path string) error {
	_, err := s.db.Exec(`UPDATE apps SET health_path = ? WHERE id = ?`, path, id)
	return err
}

// UpsertAppReplica records a slot after a swap (insert or replace by
// app+slot). Status resets to empty — a fresh container has not been probed.
func (s *Store) UpsertAppReplica(r AppReplica) error {
	_, err := s.db.Exec(
		`INSERT INTO app_replicas (id, app_id, slot, container_name, host_port, status, deploy_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, '', ?, ?)
		 ON CONFLICT (app_id, slot) DO UPDATE SET
		   container_name = excluded.container_name,
		   host_port = excluded.host_port,
		   status = '',
		   deploy_id = excluded.deploy_id,
		   updated_at = excluded.updated_at`,
		NewID(), r.AppID, r.Slot, r.ContainerName, r.HostPort, r.DeployID, Now(),
	)
	return err
}

// SetAppReplicaStatus stores the monitor's per-slot verdict. No-op when the
// slot has no row (a legacy app that has not redeployed since replicas).
func (s *Store) SetAppReplicaStatus(appID string, slot int, status string) error {
	_, err := s.db.Exec(
		`UPDATE app_replicas SET status = ?, updated_at = ? WHERE app_id = ? AND slot = ?`,
		status, Now(), appID, slot)
	return err
}

// ListAppReplicas returns an app's recorded slots ordered by slot.
func (s *Store) ListAppReplicas(appID string) ([]AppReplica, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, slot, container_name, host_port, status, deploy_id, updated_at
		 FROM app_replicas WHERE app_id = ? ORDER BY slot`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppReplica
	for rows.Next() {
		var r AppReplica
		if err := rows.Scan(&r.ID, &r.AppID, &r.Slot, &r.ContainerName, &r.HostPort, &r.Status, &r.DeployID, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteAppReplicasAbove removes the rows for slots > keep (scale-down and
// rollout convergence remove the containers first).
func (s *Store) DeleteAppReplicasAbove(appID string, keep int) error {
	_, err := s.db.Exec(`DELETE FROM app_replicas WHERE app_id = ? AND slot > ?`, appID, keep)
	return err
}

// HasActiveDeployment reports whether the app has a queued or building
// deployment — the monitor's heal/rollout mutex: mid-rollout a "down" slot
// is being replaced on purpose, and healing it would fight the rollout.
func (s *Store) HasActiveDeployment(appID string) (bool, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM deployments WHERE app_id = ? AND status IN ('queued', 'building')`,
		appID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n > 0, err
}

// HasBuildInFlight reports whether any deployment is queued or building; a
// disk cleanup waits for it so it cannot pull a build's cache out from under it.
func (s *Store) HasBuildInFlight() (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE status IN ('queued', 'building')`).Scan(&n)
	return n > 0, err
}
