package store

import (
	"database/sql"
	"errors"
)

// ErrNotFound is returned when a query matches no rows.
var ErrNotFound = errors.New("store: not found")

// CreateUser inserts a new user and returns it.
func (s *Store) CreateUser(u User) (User, error) {
	u.ID = NewID()
	u.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO users (id, email, password_hash, role, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Email, u.PasswordHash, u.Role, u.CreatedAt,
	)
	return u, err
}

// CountUsers returns the number of users in the database.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// GetUserByEmail fetches a user by email address.
func (s *Store) GetUserByEmail(email string) (User, error) {
	return s.scanUser(`SELECT id, email, password_hash, role, created_at FROM users WHERE email = ?`, email)
}

// GetUserByID fetches a user by ID.
func (s *Store) GetUserByID(id string) (User, error) {
	return s.scanUser(`SELECT id, email, password_hash, role, created_at FROM users WHERE id = ?`, id)
}

func (s *Store) scanUser(query string, arg any) (User, error) {
	var u User
	err := s.db.QueryRow(query, arg).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}
