package store

import (
	"database/sql"
	"errors"
	"time"
)

// Deployment tracks one deploy attempt: manual container runs (P2) and git
// builds (P4). The deployments table doubles as the worker queue: rows with
// status "queued" are picked up by the job worker.
type Deployment struct {
	ID            string
	AppID         string
	CommitSHA     string
	CommitMessage string
	Kind          string // deploy | rollback | manual
	Status        string // queued | building | running | failed
	ImageTag      string
	Error         string
	StartedAt     string
	FinishedAt    string
	CreatedAt     string
}

// CreateDeployment inserts a new deployment and returns it.
func (s *Store) CreateDeployment(d Deployment) (Deployment, error) {
	d.ID = NewID()
	d.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO deployments (id, app_id, commit_sha, commit_message, kind, status, image_tag, error, started_at, finished_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AppID, d.CommitSHA, d.CommitMessage, d.Kind, d.Status, d.ImageTag, d.Error,
		d.StartedAt, d.FinishedAt, d.CreatedAt,
	)
	return d, err
}

// UpdateDeployment merges mutable fields onto a deployment.
func (s *Store) UpdateDeployment(d Deployment) error {
	_, err := s.db.Exec(
		`UPDATE deployments SET status = ?, error = ?, image_tag = ?, started_at = ?, finished_at = ?, commit_sha = ?, commit_message = ? WHERE id = ?`,
		d.Status, d.Error, d.ImageTag, d.StartedAt, d.FinishedAt, d.CommitSHA, d.CommitMessage, d.ID,
	)
	return err
}

// GetDeployment fetches one deployment by ID.
func (s *Store) GetDeployment(id string) (Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(
		`SELECT id, app_id, commit_sha, commit_message, kind, status, image_tag, error, started_at, finished_at, created_at
		 FROM deployments WHERE id = ?`,
		id,
	).Scan(&d.ID, &d.AppID, &d.CommitSHA, &d.CommitMessage, &d.Kind, &d.Status, &d.ImageTag,
		&d.Error, &d.StartedAt, &d.FinishedAt, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// ClaimNextQueued atomically marks the oldest queued deployment as building
// and returns it. Returns ErrNotFound when the queue is empty.
func (s *Store) ClaimNextQueued() (Deployment, error) {
	var id string
	err := s.db.QueryRow(
		`SELECT id FROM deployments WHERE status = 'queued' ORDER BY created_at ASC LIMIT 1`,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, err
	}
	res, err := s.db.Exec(
		`UPDATE deployments SET status = 'building', started_at = ? WHERE id = ? AND status = 'queued'`,
		Now(), id,
	)
	if err != nil {
		return Deployment{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Claimed by someone else between select and update.
		return Deployment{}, ErrNotFound
	}
	return s.GetDeployment(id)
}

// FailStaleBuilding marks every deployment left in "building" by a dead
// worker as failed — a restart mid-build must not leave the queue jammed.
// Returns the number of rows reaped.
func (s *Store) FailStaleBuilding() (int64, error) {
	res, err := s.db.Exec(
		`UPDATE deployments SET status = 'failed', error = 'worker restarted mid-build — redeploy to retry', finished_at = ? WHERE status = 'building'`,
		Now(),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// LatestDeploymentOfKind returns the newest deployment of a kind for an
// app, or nil when none exists.
func (s *Store) LatestDeploymentOfKind(appID, kind string) (*Deployment, error) {
	var d Deployment
	err := s.db.QueryRow(
		`SELECT id, app_id, commit_sha, commit_message, kind, status, image_tag, error, started_at, finished_at, created_at
		 FROM deployments WHERE app_id = ? AND kind = ? ORDER BY created_at DESC LIMIT 1`,
		appID, kind,
	).Scan(&d.ID, &d.AppID, &d.CommitSHA, &d.CommitMessage, &d.Kind, &d.Status, &d.ImageTag,
		&d.Error, &d.StartedAt, &d.FinishedAt, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreatedTime parses the RFC3339 creation timestamp.
func (d *Deployment) CreatedTime() time.Time {
	t, err := time.Parse(time.RFC3339Nano, d.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ListDeployments returns an app's deployment history, newest first.
func (s *Store) ListDeployments(appID string, limit int) ([]Deployment, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, commit_sha, commit_message, kind, status, image_tag, error, started_at, finished_at, created_at
		 FROM deployments WHERE app_id = ? ORDER BY created_at DESC LIMIT ?`,
		appID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.AppID, &d.CommitSHA, &d.CommitMessage, &d.Kind, &d.Status, &d.ImageTag,
			&d.Error, &d.StartedAt, &d.FinishedAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
