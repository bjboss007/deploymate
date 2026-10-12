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
	// RepoID is GitHub's numeric repository id (0 = not known yet).
	RepoID int64
}

// CloneGitHubApp is the clone method of a source that uses the GitHub App.
const CloneGitHubApp = "github_app"

// CreateGitSource inserts a new source and returns it.
func (s *Store) CreateGitSource(gs GitSource) (GitSource, error) {
	gs.ID = NewID()
	gs.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO git_sources (id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc, installation_id, repo_full_name, repo_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		gs.ID, gs.Provider, gs.RepoURL, gs.CloneMethod, gs.PrivateKeyEnc, gs.PATEnc,
		gs.WebhookSecretEnc, gs.DefaultBranch, gs.CreatedAt, gs.APITokenEnc, gs.InstallationID, gs.RepoFullName, gs.RepoID,
	)
	return gs, err
}

// GetGitSource fetches a source by ID.
func (s *Store) GetGitSource(id string) (GitSource, error) {
	var gs GitSource
	err := s.db.QueryRow(
		`SELECT id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc, installation_id, repo_full_name, repo_id
		 FROM git_sources WHERE id = ?`,
		id,
	).Scan(&gs.ID, &gs.Provider, &gs.RepoURL, &gs.CloneMethod, &gs.PrivateKeyEnc, &gs.PATEnc,
		&gs.WebhookSecretEnc, &gs.DefaultBranch, &gs.CreatedAt, &gs.APITokenEnc, &gs.InstallationID, &gs.RepoFullName, &gs.RepoID)
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

const gitSourceCols = `id, provider, repo_url, clone_method, private_key_enc, pat_enc, webhook_secret_enc, default_branch, created_at, api_token_enc, installation_id, repo_full_name, repo_id`

func (s *Store) listGitSources(where string, args ...any) ([]GitSource, error) {
	rows, err := s.db.Query(`SELECT `+gitSourceCols+` FROM git_sources WHERE clone_method = ? AND `+where+` ORDER BY created_at`,
		append([]any{CloneGitHubApp}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GitSource
	for rows.Next() {
		var gs GitSource
		if err := rows.Scan(&gs.ID, &gs.Provider, &gs.RepoURL, &gs.CloneMethod, &gs.PrivateKeyEnc, &gs.PATEnc,
			&gs.WebhookSecretEnc, &gs.DefaultBranch, &gs.CreatedAt, &gs.APITokenEnc, &gs.InstallationID, &gs.RepoFullName, &gs.RepoID); err != nil {
			return nil, err
		}
		out = append(out, gs)
	}
	return out, rows.Err()
}

// ListGitHubAppSources returns every source created through Connect GitHub.
func (s *Store) ListGitHubAppSources() ([]GitSource, error) { return s.listGitSources("1 = 1") }

// ListGitSourcesByRepoID returns the Connect GitHub sources for a GitHub repository id.
func (s *Store) ListGitSourcesByRepoID(id int64) ([]GitSource, error) {
	return s.listGitSources("repo_id = ?", id)
}

// ListUnidentifiedGitSourcesByName returns the Connect GitHub sources whose repository id is
// not known yet, by repository name ("owner/name", any case): the ones made before ids were
// stored, matched by the only thing they have. Sources that know their id are never matched
// by name, because a name can be reused by a different repository after a rename.
func (s *Store) ListUnidentifiedGitSourcesByName(fullName string) ([]GitSource, error) {
	return s.listGitSources("repo_id = 0 AND repo_full_name = ?", strings.ToLower(fullName))
}

// UpdateGitSourceRepo records what GitHub says the repository is now: its id, name and clone
// address (a rename or a transfer changes the last two, never the first).
func (s *Store) UpdateGitSourceRepo(id string, repoID int64, fullName, repoURL string) error {
	_, err := s.db.Exec(`UPDATE git_sources SET repo_id = ?, repo_full_name = ?, repo_url = ? WHERE id = ?`,
		repoID, strings.ToLower(fullName), repoURL, id)
	return err
}
