package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// App is a runnable thing: a manual container (image set), or a
// git-connected buildable app — built from its Dockerfile or from a
// selected runtime (Runtime != "", see builder.Runtimes).
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
	Runtime             string // "" = Dockerfile; else "node:22", "python", ...
	Environment         string // EnvProduction | EnvStaging — selects which services and manifest apply
	Health              string // healthy | unhealthy | "" (monitor-maintained)
	MemLimitMB          int    // detected limit; 0 = not yet detected
	CPULimit            float64
	PreviewHostPort     int    // loopback port the current container publishes; 0 = deterministic hash
	Entrypoint          string // image-deploy override; whitespace-separated; "" = image default
	Command             string // image-deploy override; whitespace-separated; "" = image default
	CreatedAt           string
	Replicas            int    // desired slot count, 1..MaxReplicas (docs/specs/app-replicas.md)
	HealthPath          string // per-slot probe + Traefik healthcheck path; default "/"
	// Prebuilt deploys (docs/specs/prebuilt-deploys.md). DeployMode is
	// DeployModeBuild (default) or DeployModeArtifact; the other two say
	// which GitHub workflow's runs deploy this app and which uploaded
	// artifact holds its JAR.
	DeployMode   string
	WorkflowPath string
	ArtifactName string

	// Appearance (migration 0018): Logo and Accent are the owner's choices
	// ("" = automatic); Stack is the framework detected at deploy time.
	Logo   string
	Accent string
	Stack  string
}

// Deploy modes (apps.deploy_mode).
const (
	DeployModeBuild    = "build"    // clone + build on this server (the original behavior)
	DeployModeArtifact = "artifact" // deploy a JAR built by GitHub Actions
)

// Defaults for the prebuilt-deploy settings (migration 0017's column defaults).
const (
	DefaultWorkflowPath = ".github/workflows/deploymate.yml"
	DefaultArtifactName = "deploymate-app"
)

// appColumns is the column list every app SELECT reads, in scan order.
const appColumns = `id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, runtime, environment, health, mem_limit_mb, cpu_limit, preview_host_port, entrypoint, command, created_at, replicas, health_path, deploy_mode, workflow_path, artifact_name, logo, accent, stack`

// ErrSlugTaken is returned when a slug is already in use.
var ErrSlugTaken = errors.New("store: slug already exists")

// CreateApp inserts a new app and returns it.
func (s *Store) CreateApp(a App) (App, error) {
	a.ID = NewID()
	a.CreatedAt = Now()
	if a.Environment == "" {
		a.Environment = EnvDev
	}
	a.Replicas = ClampReplicas(a.Replicas)
	if a.HealthPath == "" {
		a.HealthPath = DefaultHealthPath
	}
	if a.DeployMode == "" {
		a.DeployMode = DeployModeBuild
	}
	if a.WorkflowPath == "" {
		a.WorkflowPath = DefaultWorkflowPath
	}
	if a.ArtifactName == "" {
		a.ArtifactName = DefaultArtifactName
	}
	// Empty strings violate the git_sources foreign key; NULL is correct.
	var gitSourceID any
	if a.GitSourceID != "" {
		gitSourceID = a.GitSourceID
	}
	_, err := s.db.Exec(
		`INSERT INTO apps (id, project_id, name, slug, git_source_id, build_type, root_directory, status, current_deployment_id, image, port, runtime, environment, health, mem_limit_mb, cpu_limit, preview_host_port, entrypoint, command, created_at, replicas, health_path, deploy_mode, workflow_path, artifact_name, logo, accent, stack)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ProjectID, a.Name, a.Slug, gitSourceID, a.BuildType, a.RootDirectory,
		a.Status, a.CurrentDeploymentID, a.Image, a.Port, a.Runtime, a.Environment, a.Health, a.MemLimitMB, a.CPULimit, a.PreviewHostPort, a.Entrypoint, a.Command, a.CreatedAt, a.Replicas, a.HealthPath, a.DeployMode, a.WorkflowPath, a.ArtifactName, a.Logo, a.Accent, a.Stack,
	)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return a, ErrSlugTaken
	}
	return a, err
}

// ListApps returns all apps in a project, newest first.
func (s *Store) ListApps(projectID string) ([]App, error) {
	rows, err := s.db.Query(
		`SELECT `+appColumns+`
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
		`SELECT `+appColumns+`
		 FROM apps WHERE slug = ?`,
		slug,
	).Scan(&a.ID, &a.ProjectID, &a.Name, &a.Slug, &gitSourceID, &a.BuildType, &a.RootDirectory,
		&a.Status, &a.CurrentDeploymentID, &a.Image, &a.Port, &a.Runtime, &a.Environment, &a.Health, &a.MemLimitMB, &a.CPULimit, &a.PreviewHostPort, &a.Entrypoint, &a.Command, &a.CreatedAt, &a.Replicas, &a.HealthPath, &a.DeployMode, &a.WorkflowPath, &a.ArtifactName, &a.Logo, &a.Accent, &a.Stack)
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

// UpdateAppDeployConfig persists the deploy form (image, port, and the
// entrypoint/command overrides) so start/restart, heals, rollbacks, and
// future deploys rebuild from this row. Empty entrypoint/command mean
// "image default".
func (s *Store) UpdateAppDeployConfig(id, image string, port int, entrypoint, command string) error {
	_, err := s.db.Exec(
		`UPDATE apps SET image = ?, port = ?, entrypoint = ?, command = ? WHERE id = ?`,
		image, port, entrypoint, command, id)
	return err
}

// UpdateAppPort sets the routing port for an app (used for health checks
// and Traefik routing).
func (s *Store) UpdateAppPort(id string, port int) error {
	_, err := s.db.Exec(`UPDATE apps SET port = ? WHERE id = ?`, port, id)
	return err
}

// UpdateAppDeployMode sets how the app is deployed and, for prebuilt
// (artifact) mode, which workflow and artifact feed it. Empty workflow/
// artifact values fall back to the defaults.
func (s *Store) UpdateAppDeployMode(id, mode, workflowPath, artifactName string) error {
	if mode != DeployModeBuild && mode != DeployModeArtifact {
		return fmt.Errorf("store: unknown deploy mode %q", mode)
	}
	if workflowPath == "" {
		workflowPath = DefaultWorkflowPath
	}
	if artifactName == "" {
		artifactName = DefaultArtifactName
	}
	_, err := s.db.Exec(`UPDATE apps SET deploy_mode = ?, workflow_path = ?, artifact_name = ? WHERE id = ?`,
		mode, workflowPath, artifactName, id)
	return err
}

// UpdateAppRuntime sets the build method: "" for Dockerfile, or a
// runtime spec like "node:22" for Railpack builds.
func (s *Store) UpdateAppRuntime(id, runtime string) error {
	_, err := s.db.Exec(`UPDATE apps SET runtime = ? WHERE id = ?`, runtime, id)
	return err
}

// UpdateAppRootDirectory sets the repository subfolder the app builds from ("" = the
// repository root). The caller validates it (see httpserver.cleanRootDirectory).
func (s *Store) UpdateAppRootDirectory(id, dir string) error {
	_, err := s.db.Exec(`UPDATE apps SET root_directory = ? WHERE id = ?`, dir, id)
	return err
}

// UpdateAppEnvironment sets the app's environment: EnvProduction or
// EnvStaging. The next deploy uses that environment's manifest overlay
// and services.
func (s *Store) UpdateAppEnvironment(id, environment string) error {
	_, err := s.db.Exec(`UPDATE apps SET environment = ? WHERE id = ?`, environment, id)
	return err
}

// UpdateAppHealth sets the monitor-maintained health state.
func (s *Store) UpdateAppHealth(id, health string) error {
	_, err := s.db.Exec(`UPDATE apps SET health = ? WHERE id = ?`, health, id)
	return err
}

// UpdateAppPreviewPort records the loopback host port the app's current
// container publishes. 0 clears it (back to the deterministic hash).
func (s *Store) UpdateAppPreviewPort(id string, port int) error {
	_, err := s.db.Exec(`UPDATE apps SET preview_host_port = ? WHERE id = ?`, port, id)
	return err
}

// UpdateAppResources sets the detected resource limits.
func (s *Store) UpdateAppResources(id string, memLimitMB int, cpuLimit float64) error {
	_, err := s.db.Exec(`UPDATE apps SET mem_limit_mb = ?, cpu_limit = ? WHERE id = ?`, memLimitMB, cpuLimit, id)
	return err
}

// GetAppByID fetches an app by ID.
func (s *Store) GetAppByID(id string) (App, error) {
	var a App
	var gitSourceID sql.NullString
	err := s.db.QueryRow(
		`SELECT `+appColumns+`
		 FROM apps WHERE id = ?`,
		id,
	).Scan(&a.ID, &a.ProjectID, &a.Name, &a.Slug, &gitSourceID, &a.BuildType, &a.RootDirectory,
		&a.Status, &a.CurrentDeploymentID, &a.Image, &a.Port, &a.Runtime, &a.Environment, &a.Health, &a.MemLimitMB, &a.CPULimit, &a.PreviewHostPort, &a.Entrypoint, &a.Command, &a.CreatedAt, &a.Replicas, &a.HealthPath, &a.DeployMode, &a.WorkflowPath, &a.ArtifactName, &a.Logo, &a.Accent, &a.Stack)
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
		`SELECT ` + appColumns + `
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
		`SELECT `+appColumns+`
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
			&a.RootDirectory, &a.Status, &a.CurrentDeploymentID, &a.Image, &a.Port, &a.Runtime, &a.Environment, &a.Health, &a.MemLimitMB, &a.CPULimit, &a.PreviewHostPort, &a.Entrypoint, &a.Command, &a.CreatedAt, &a.Replicas, &a.HealthPath, &a.DeployMode, &a.WorkflowPath, &a.ArtifactName, &a.Logo, &a.Accent, &a.Stack); err != nil {
			return nil, err
		}
		a.GitSourceID = gitSourceID.String
		out = append(out, a)
	}
	return out, rows.Err()
}


// UpdateAppAppearance saves the owner's logo and identity-colour choices
// ("" = automatic for each).
func (s *Store) UpdateAppAppearance(id, logo, accent string) error {
	_, err := s.db.Exec(`UPDATE apps SET logo = ?, accent = ? WHERE id = ?`, logo, accent, id)
	return err
}

// UpdateAppStack records the framework detected at deploy time ("" clears it).
func (s *Store) UpdateAppStack(id, stack string) error {
	_, err := s.db.Exec(`UPDATE apps SET stack = ? WHERE id = ?`, stack, id)
	return err
}
