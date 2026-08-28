package store

import (
	"database/sql"
	"errors"
)

// CreateProject inserts a new project and returns it.
func (s *Store) CreateProject(p Project) (Project, error) {
	p.ID = NewID()
	p.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO projects (id, user_id, name, slug, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.ID, p.UserID, p.Name, p.Slug, p.CreatedAt,
	)
	return p, err
}

// ListProjects returns all projects for a user, newest first.
func (s *Store) ListProjects(userID string) ([]Project, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, slug, created_at FROM projects WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanProjects(rows)
}

// GetProjectBySlug fetches a project by its slug (user-scoped).
func (s *Store) GetProjectBySlug(userID, slug string) (Project, error) {
	var p Project
	err := s.db.QueryRow(
		`SELECT id, user_id, name, slug, created_at FROM projects WHERE user_id = ? AND slug = ?`,
		userID, slug,
	).Scan(&p.ID, &p.UserID, &p.Name, &p.Slug, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// GetProjectByID fetches a project by ID.
func (s *Store) GetProjectByID(id string) (Project, error) {
	var p Project
	err := s.db.QueryRow(
		`SELECT id, user_id, name, slug, created_at FROM projects WHERE id = ?`,
		id,
	).Scan(&p.ID, &p.UserID, &p.Name, &p.Slug, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// DeleteProject removes a project (apps and services cascade).
func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}

func scanProjects(rows *sql.Rows) ([]Project, error) {
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.Slug, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
