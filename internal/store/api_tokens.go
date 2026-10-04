package store

import (
	"database/sql"
	"errors"
	"time"
)

// API token scopes, each including the ones before it.
const (
	ScopeRead      = "read"      // monitor: GET/HEAD only
	ScopeDeploy    = "deploy"    // + act on apps that exist (deploy, retry, redeploy, restart…)
	ScopeProvision = "provision" // + create and configure (projects, apps, services, variables, domains)
)

// ScopeRank orders scopes; an unknown scope ranks 0 and so can do nothing. The
// "write" scope of the first token page (before the three tiers) is kept as
// deploy, never silently widened to provision.
func ScopeRank(scope string) int {
	switch scope {
	case ScopeRead:
		return 1
	case ScopeDeploy, "write":
		return 2
	case ScopeProvision:
		return 3
	}
	return 0
}

// ValidScope reports whether scope is one a new token may be created with.
func ValidScope(scope string) bool {
	return scope == ScopeRead || scope == ScopeDeploy || scope == ScopeProvision
}

// APIToken is a personal access token. The plaintext is never stored: only
// TokenHash (see auth.HashToken) and a short Prefix to recognise it by.
type APIToken struct {
	ID         string
	UserID     string
	Name       string
	TokenHash  string
	Prefix     string
	Scope      string
	CreatedAt  string
	LastUsedAt string
	ExpiresAt  string // "" = never
}

// Expired reports whether the token is past its expiry at now.
func (t APIToken) Expired(now time.Time) bool {
	if t.ExpiresAt == "" {
		return false
	}
	exp, err := time.Parse(time.RFC3339Nano, t.ExpiresAt)
	return err != nil || !now.Before(exp) // an unreadable expiry counts as expired
}

const apiTokenColumns = `id, user_id, name, token_hash, prefix, scope, created_at, last_used_at, expires_at`

func (t *APIToken) scanFields() []any {
	return []any{&t.ID, &t.UserID, &t.Name, &t.TokenHash, &t.Prefix, &t.Scope, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt}
}

// CreateAPIToken stores a new token (hash only).
func (s *Store) CreateAPIToken(t APIToken) (APIToken, error) {
	t.ID = NewID()
	t.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO api_tokens (`+apiTokenColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.UserID, t.Name, t.TokenHash, t.Prefix, t.Scope, t.CreatedAt, t.LastUsedAt, t.ExpiresAt)
	return t, err
}

// ListAPITokens returns a user's tokens, newest first.
func (s *Store) ListAPITokens(userID string) ([]APIToken, error) {
	rows, err := s.db.Query(`SELECT `+apiTokenColumns+` FROM api_tokens WHERE user_id = ? ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(t.scanFields()...); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetAPITokenByHash looks a token up by the hash of its plaintext.
func (s *Store) GetAPITokenByHash(hash string) (APIToken, error) {
	var t APIToken
	err := s.db.QueryRow(`SELECT `+apiTokenColumns+` FROM api_tokens WHERE token_hash = ?`, hash).Scan(t.scanFields()...)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// DeleteAPIToken revokes a token. The user id makes it impossible to revoke
// someone else's by guessing an id. It reports whether a row was removed.
func (s *Store) DeleteAPIToken(id, userID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TouchAPIToken records a use, at most once a minute per token so a busy
// client does not write on every request.
func (s *Store) TouchAPIToken(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at = '' OR last_used_at < ?)`,
		now.Format(time.RFC3339Nano), id, now.Add(-time.Minute).Format(time.RFC3339Nano))
	return err
}

// CountAPITokens is how many tokens a user has (the page caps it).
func (s *Store) CountAPITokens(userID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`, userID).Scan(&n)
	return n, err
}
