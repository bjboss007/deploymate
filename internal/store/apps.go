package store

import (
	"database/sql"
	"errors"
	"strings"
)

// App is a runnable thing: a manual container (image set) now, a
// git-connected buildable app (git_source_id set) from P4 on.
type App struct {
	ID                  string
	ProjectID           string
	Name                string
	Slug                string
	GitSourceID         string
	BuildType           string
	RootDirectory       string
	Status              string
	CurrentDeploymentID string
	Image               string
	Port                int
	CreatedAt           string
}

// ErrSlugTaken is returned when a slug is already in use.
var ErrSlugTaken = errors.New("store: slug already exists")

// CreateApp inserts a new app and returns it.
func (s *Store) CreateApp(a App) (App, error) {
	a.ID = NewID()
	a.CreatedAt = Now()
	// Empty strings violate the git_sources foreign key; NULL is correct.
	var gitSourceID any
	if a.GitSourceID != "" {
		gitSourceID = a.GitSourceID
	}
	_, err := s.db.Exec(
		`INSERT INTO apps (id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.Name, a.Slug, gitSourceID, a.BuildType, a.RootDirectory,
		a.Status, a.CurrentDeploymentID, a.Image, a.Port, a.CreatedAt,
	)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return a, ErrSlugTaken
	}
	return a, err
}

// ListApps returns all apps in a project, newest first.
func (s *Store) ListApps(projectID string) ([]App, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, created_at
		 FROM apps WHERE project_id = ? ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApps(rows)
}

// GetAppBySlug fetches an app by slug (globally unique).
func (s *Store) GetAppBySlug(slug string) (App, error) {
	var a App
	var gitSourceID sql.NullString
	err := s.db.QueryRow(
		`SELECT id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, created_at
		 FROM apps WHERE slug = ?`,
		slug,
	).Scan(&a.ID, &a.ProjectID, &a.Name, &a.Slug, &gitSourceID, &a.BuildType, &a.RootDirectory,
		&a.Status, &a.CurrentDeploymentID, &a.Image, &a.Port, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.GitSourceID = gitSourceID.String
	return a, err
}

// UpdateAppStatus sets the app's runtime status.
func (s *Store) UpdateAppStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE apps SET status = ? WHERE id = ?`, status, id)
	return err
}

// UpdateAppImagePort persists the image/port chosen at deploy time so
// start/stop and future deploys keep working.
func (s *Store) UpdateAppImagePort(id, image string, port int) error {
	_, err := s.db.Exec(`UPDATE apps SET image = ?, port = ? WHERE id = ?`, image, port, id)
	return err
}

// UpdateAppPort sets the routing port for an app (used for health checks
// and Traefik routing).
func (s *Store) UpdateAppPort(id string, port int) error {
	_, err := s.db.Exec(`UPDATE apps SET port = ? WHERE id = ?`, port, id)
	return err
}

// GetAppByID fetches an app by ID.
func (s *Store) GetAppByID(id string) (App, error) {
	var a App
	var gitSourceID sql.NullString
	err := s.db.QueryRow(
		`SELECT id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, created_at
		 FROM apps WHERE id = ?`,
		id,
	).Scan(&a.ID, &a.ProjectID, &a.Name, &a.Slug, &gitSourceID, &a.BuildType, &a.RootDirectory,
		&a.Status, &a.CurrentDeploymentID, &a.Image, &a.Port, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.GitSourceID = gitSourceID.String
	return a, err
}

// SetAppCurrentDeployment points the app at its active deployment.
func (s *Store) SetAppCurrentDeployment(appID, deploymentID string) error {
	_, err := s.db.Exec(`UPDATE apps SET current_deployment_id = ? WHERE id = ?`, deploymentID, appID)
	return err
}

// ListAllApps returns every app across projects (monitor use).
func (s *Store) ListAllApps() ([]App, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, created_at
		 FROM apps ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApps(rows)
}

// ListAppsByGitSource returns apps linked to a git source.
func (s *Store) ListAppsByGitSource(gitSourceID string) ([]App, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, created_at
		 FROM apps WHERE git_source_id = ?`,
		gitSourceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApps(rows)
}

// DeleteApp removes an app (deployments, env vars, domains cascade).
func (s *Store) DeleteApp(id string) error {
	_, err := s.db.Exec(`DELETE FROM apps WHERE id = ?`, id)
	return err
}

func scanApps(rows *sql.Rows) ([]App, error) {
	var out []App
	for rows.Next() {
		var a App
		var gitSourceID sql.NullString
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.Name, &a.Slug, &gitSourceID, &a.BuildType,
			&a.RootDirectory, &a.Status, &a.CurrentDeploymentID, &a.Image, &a.Port, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.GitSourceID = gitSourceID.String
		out = append(out, a)
	}
	return out, rows.Err()
}
