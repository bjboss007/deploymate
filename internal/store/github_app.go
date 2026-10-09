package store

import (
	"database/sql"
	"errors"
)

// GitHubApp is the app DeployMate registered on GitHub. Secrets are encrypted.
type GitHubApp struct {
	AppID            int64
	Slug             string
	Name             string
	HTMLURL          string
	OwnerLogin       string
	OwnerType        string
	ClientID         string
	ClientSecretEnc  string
	WebhookSecretEnc string
	PEMEnc           string
	WebhookURL       string
	CreatedAt        string
}

// GetGitHubApp returns the connected app, or ErrNotFound.
func (s *Store) GetGitHubApp() (GitHubApp, error) {
	var a GitHubApp
	err := s.db.QueryRow(`SELECT app_id, slug, name, html_url, owner_login, owner_type, client_id,
		client_secret_enc, webhook_secret_enc, pem_enc, webhook_url, created_at FROM github_app WHERE id = 1`).
		Scan(&a.AppID, &a.Slug, &a.Name, &a.HTMLURL, &a.OwnerLogin, &a.OwnerType, &a.ClientID,
			&a.ClientSecretEnc, &a.WebhookSecretEnc, &a.PEMEnc, &a.WebhookURL, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GitHubApp{}, ErrNotFound
	}
	return a, err
}

// SaveGitHubApp stores the connected app, replacing any earlier one.
func (s *Store) SaveGitHubApp(a GitHubApp) error {
	if a.CreatedAt == "" {
		a.CreatedAt = Now()
	}
	_, err := s.db.Exec(`INSERT INTO github_app (id, app_id, slug, name, html_url, owner_login, owner_type, client_id,
		client_secret_enc, webhook_secret_enc, pem_enc, webhook_url, created_at)
		VALUES (1,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET app_id=excluded.app_id, slug=excluded.slug, name=excluded.name,
		  html_url=excluded.html_url, owner_login=excluded.owner_login, owner_type=excluded.owner_type,
		  client_id=excluded.client_id, client_secret_enc=excluded.client_secret_enc,
		  webhook_secret_enc=excluded.webhook_secret_enc, pem_enc=excluded.pem_enc,
		  webhook_url=excluded.webhook_url, created_at=excluded.created_at`,
		a.AppID, a.Slug, a.Name, a.HTMLURL, a.OwnerLogin, a.OwnerType, a.ClientID,
		a.ClientSecretEnc, a.WebhookSecretEnc, a.PEMEnc, a.WebhookURL, a.CreatedAt)
	return err
}

// DeleteGitHubApp forgets the connection (the app itself stays on GitHub until
// the owner deletes it there).
func (s *Store) DeleteGitHubApp() error {
	_, err := s.db.Exec(`DELETE FROM github_app WHERE id = 1`)
	return err
}
