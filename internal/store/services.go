package store

import (
	"database/sql"
	"errors"
	"strings"
)

// Service is a managed database or cache: Postgres, MySQL, or Redis.
type Service struct {
	ID          string
	ProjectID   string
	Type        string
	Name        string
	Slug        string
	Image       string
	Status      string
	VolumeName  string
	Port        int
	Environment string // EnvProduction | EnvStaging — only apps in the same environment see this service
	Origin      string // OriginManual | OriginManifest — who created it
	Orphaned    bool   // manifest-created but no longer declared; surfaced for a human to delete or keep
	CreatedAt   string
}

// CreateService inserts a new service and returns it. Empty environment
// normalizes to production (manual services are production).
func (s *Store) CreateService(sv Service) (Service, error) {
	sv.ID = NewID()
	sv.CreatedAt = Now()
	if sv.Environment == "" {
		sv.Environment = EnvProduction
	}
	if sv.Origin == "" {
		sv.Origin = OriginManual
	}
	_, err := s.db.Exec(
		`INSERT INTO services (id, project_id, type, name, slug, image, status, volume_name, port, environment, origin, orphaned, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sv.ID, sv.ProjectID, sv.Type, sv.Name, sv.Slug, sv.Image, sv.Status, sv.VolumeName, sv.Port, sv.Environment, sv.Origin, sv.Orphaned, sv.CreatedAt,
	)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return sv, ErrSlugTaken
	}
	return sv, err
}

// ListServices returns all services in a project, newest first.
func (s *Store) ListServices(projectID string) ([]Service, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, type, name, slug, image, status, volume_name, port, environment, origin, orphaned, created_at
		 FROM services WHERE project_id = ? ORDER BY created_at DESC`,
		projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServices(rows)
}

// GetServiceBySlug fetches a service by slug (globally unique).
func (s *Store) GetServiceBySlug(slug string) (Service, error) {
	var sv Service
	err := s.db.QueryRow(
		`SELECT id, project_id, type, name, slug, image, status, volume_name, port, environment, origin, orphaned, created_at
		 FROM services WHERE slug = ?`,
		slug,
	).Scan(&sv.ID, &sv.ProjectID, &sv.Type, &sv.Name, &sv.Slug, &sv.Image, &sv.Status,
		&sv.VolumeName, &sv.Port, &sv.Environment, &sv.Origin, &sv.Orphaned, &sv.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sv, ErrNotFound
	}
	return sv, err
}

// UpdateServiceStatus sets the service's runtime status.
func (s *Store) UpdateServiceStatus(id, status string) error {
	_, err := s.db.Exec(`UPDATE services SET status = ? WHERE id = ?`, status, id)
	return err
}

// SetServiceOrphaned raises or clears a service's orphaned flag. A manifest
// deploy raises it on a manifest-created service it no longer declares, and
// clears it when that type is declared again.
func (s *Store) SetServiceOrphaned(id string, orphaned bool) error {
	_, err := s.db.Exec(`UPDATE services SET orphaned = ? WHERE id = ?`, orphaned, id)
	return err
}

// AdoptService is the "Keep" action: a human claims an orphaned,
// manifest-created service as their own. Origin flips to manual so future
// deploys stop flagging it, and the orphaned flag clears.
func (s *Store) AdoptService(id string) error {
	_, err := s.db.Exec(`UPDATE services SET origin = ?, orphaned = 0 WHERE id = ?`, OriginManual, id)
	return err
}

// DeleteService removes a service. The named volume is deliberately kept —
// deleting data should be an explicit act.
func (s *Store) DeleteService(id string) error {
	_, err := s.db.Exec(`DELETE FROM services WHERE id = ?`, id)
	return err
}

func scanServices(rows *sql.Rows) ([]Service, error) {
	var out []Service
	for rows.Next() {
		var sv Service
		if err := rows.Scan(&sv.ID, &sv.ProjectID, &sv.Type, &sv.Name, &sv.Slug, &sv.Image,
			&sv.Status, &sv.VolumeName, &sv.Port, &sv.Environment, &sv.Origin, &sv.Orphaned, &sv.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// --- credentials --------------------------------------------------------

// ServiceCredential is a generated secret (e.g. database password), value
// encrypted at rest.
type ServiceCredential struct {
	ID        string
	ServiceID string
	Key       string
	ValueEnc  string
	Value     string // decrypted form, filled by the caller when needed
}

// SetServiceCredentials replaces all credentials for a service. Values are
// stored exactly as given — callers pass encrypted blobs.
func (s *Store) SetServiceCredentials(serviceID string, creds map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM service_credentials WHERE service_id = ?`, serviceID); err != nil {
		return err
	}
	for k, v := range creds {
		if _, err := tx.Exec(
			`INSERT INTO service_credentials (id, service_id, key, value_enc) VALUES (?, ?, ?, ?)`,
			NewID(), serviceID, k, v,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetServiceCredentials returns the stored (encrypted) credentials as a map;
// callers decrypt.
func (s *Store) GetServiceCredentials(serviceID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value_enc FROM service_credentials WHERE service_id = ?`, serviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var k, vEnc string
		if err := rows.Scan(&k, &vEnc); err != nil {
			return nil, err
		}
		out[k] = vEnc
	}
	return out, rows.Err()
}
