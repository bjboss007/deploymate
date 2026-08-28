package store

import (
	"database/sql"
	"errors"
)

// CreateSession inserts a new session record.
func (s *Store) CreateSession(sess Session) (Session, error) {
	sess.ID = NewID()
	sess.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO sessions (id, user_id, token_hash, csrf_token, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.TokenHash, sess.CSRFToken, sess.ExpiresAt, sess.CreatedAt,
	)
	return sess, err
}

// GetSessionByTokenHash fetches a session by SHA-256 of its cookie token.
func (s *Store) GetSessionByTokenHash(tokenHash string) (Session, error) {
	var sess Session
	err := s.db.QueryRow(
		`SELECT id, user_id, token_hash, csrf_token, expires_at, created_at FROM sessions WHERE token_hash = ?`,
		tokenHash,
	).Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.CSRFToken, &sess.ExpiresAt, &sess.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, ErrNotFound
	}
	return sess, err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteExpiredSessions prunes sessions past their expiry.
func (s *Store) DeleteExpiredSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, Now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
