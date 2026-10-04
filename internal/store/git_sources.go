package store

import (
	"database/sql"
	"errors"
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
}

// CreateGitSource inserts a new source and returns it.
func (s *Store) CreateGitSource(gs GitSource) (GitSource, error) {
	gs.ID = NewID()
	gs.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO git_sources (id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gs.ID, gs.Provider, gs.RepoURL, gs.CloneMethod, gs.PrivateKeyEnc, gs.PATEnc,
		gs.WebhookSecretEnc, gs.DefaultBranch, gs.CreatedAt, gs.APITokenEnc,
	)
	return gs, err
}

// GetGitSource fetches a source by ID.
func (s *Store) GetGitSource(id string) (GitSource, error) {
	var gs GitSource
	err := s.db.QueryRow(
		`SELECT id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc
		 FROM git_sources WHERE id = ?`,
		id,
	).Scan(&gs.ID, &gs.Provider, &gs.RepoURL, &gs.CloneMethod, &gs.PrivateKeyEnc, &gs.PATEnc,
		&gs.WebhookSecretEnc, &gs.DefaultBranch, &gs.CreatedAt, &gs.APITokenEnc)
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
