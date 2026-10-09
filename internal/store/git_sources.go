package store

import (
	"database/sql"
	"errors"
	"strings"
)

// GitSource is a connected repository: credentials (encrypted at rest by the
// caller), webhook secret, and the branch deploys track.
type GitSource struct {
	ID               string
	Provider         string // github | gitlab | gitea
	RepoURL          string
	CloneMethod      string // deploy_key | pat
	PrivateKeyEnc    string
	PATEnc           string
	WebhookSecretEnc string
	DefaultBranch    string
	CreatedAt        string
	// APITokenEnc is the encrypted fine-grained GitHub token (Actions: read)
	// prebuilt deploys use to list runs and download artifacts; "" = none.
	APITokenEnc string
	// InstallationID and RepoFullName ("owner/name", lower case) are set for sources
	// created through Connect GitHub (CloneMethod "github_app"); 0 and "" otherwise.
	InstallationID int64
	RepoFullName   string
}

// CloneGitHubApp is the clone method of a source that uses the GitHub App.
const CloneGitHubApp = "github_app"

// CreateGitSource inserts a new source and returns it.
func (s *Store) CreateGitSource(gs GitSource) (GitSource, error) {
	gs.ID = NewID()
	gs.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO git_sources (id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc, installation_id, repo_full_name)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gs.ID, gs.Provider, gs.RepoURL, gs.CloneMethod, gs.PrivateKeyEnc, gs.PATEnc,
		gs.WebhookSecretEnc, gs.DefaultBranch, gs.CreatedAt, gs.APITokenEnc, gs.InstallationID, gs.RepoFullName,
	)
	return gs, err
}

// GetGitSource fetches a source by ID.
func (s *Store) GetGitSource(id string) (GitSource, error) {
	var gs GitSource
	err := s.db.QueryRow(
		`SELECT id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc, installation_id, repo_full_name
		 FROM git_sources WHERE id = ?`,
		id,
	).Scan(&gs.ID, &gs.Provider, &gs.RepoURL, &gs.CloneMethod, &gs.PrivateKeyEnc, &gs.PATEnc,
		&gs.WebhookSecretEnc, &gs.DefaultBranch, &gs.CreatedAt, &gs.APITokenEnc, &gs.InstallationID, &gs.RepoFullName)
	if errors.Is(err, sql.ErrNoRows) {
		return gs, ErrNotFound
	}
	return gs, err
}

// SetGitSourceAPIToken stores (or, with "", clears) the encrypted GitHub API
// token for prebuilt deploys.
func (s *Store) SetGitSourceAPIToken(id, tokenEnc string) error {
	_, err := s.db.Exec(`UPDATE git_sources SET api_token_enc = ? WHERE id = ?`, tokenEnc, id)
	return err
}

// SetGitSourceWebhookSecret replaces the (encrypted) webhook secret.
func (s *Store) SetGitSourceWebhookSecret(id, secretEnc string) error {
	_, err := s.db.Exec(`UPDATE git_sources SET webhook_secret_enc = ? WHERE id = ?`, secretEnc, id)
	return err
}

// UpdateAppGitSource links (or unlinks) an app to a git source.
func (s *Store) UpdateAppGitSource(appID, gitSourceID string) error {
	if gitSourceID == "" {
		_, err := s.db.Exec(`UPDATE apps SET git_source_id = NULL WHERE id = ?`, appID)
		return err
	}
	_, err := s.db.Exec(`UPDATE apps SET git_source_id = ? WHERE id = ?`, gitSourceID, appID)
	return err
}

// ListGitSourcesByRepo returns the sources created through Connect GitHub for a
// repository ("owner/name", any case): the apps a GitHub event about it concerns.
func (s *Store) ListGitSourcesByRepo(fullName string) ([]GitSource, error) {
	rows, err := s.db.Query(
		`SELECT id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc, installation_id, repo_full_name
		 FROM git_sources WHERE clone_method = ? AND repo_full_name = ? ORDER BY created_at`,
		CloneGitHubApp, strings.ToLower(fullName))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitSource
	for rows.Next() {
		var gs GitSource
		if err := rows.Scan(&gs.ID, &gs.Provider, &gs.RepoURL, &gs.CloneMethod, &gs.PrivateKeyEnc, &gs.PATEnc,
			&gs.WebhookSecretEnc, &gs.DefaultBranch, &gs.CreatedAt, &gs.APITokenEnc, &gs.InstallationID, &gs.RepoFullName); err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, rows.Err()
}
