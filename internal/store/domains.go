package store

// Domain routes hostname traffic to an app with automatic TLS.
type Domain struct {
	ID            string
	AppID         string
	Hostname      string
	IsPrimary     bool
	TLSStatus     string // pending | active | expiring | untrusted | failed (see internal/tlscheck)
	CertExpiresAt string
	CreatedAt     string
}

// CreateDomain adds a domain for an app.
func (s *Store) CreateDomain(d Domain) (Domain, error) {
	d.ID = NewID()
	d.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO domains (id, app_id, hostname, is_primary, tls_status, cert_expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.AppID, d.Hostname, d.IsPrimary, d.TLSStatus, d.CertExpiresAt, d.CreatedAt,
	)
	return d, err
}

// UpdateDomainTLS records what the last certificate check saw: the status and
// the certificate's expiry ("" when there was no usable certificate).
func (s *Store) UpdateDomainTLS(id, status, expiresAt string) error {
	_, err := s.db.Exec(`UPDATE domains SET tls_status = ?, cert_expires_at = ? WHERE id = ?`, status, expiresAt, id)
	return err
}

// ListDomains returns an app's domains, oldest first.
func (s *Store) ListDomains(appID string) ([]Domain, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, hostname, is_primary, tls_status, cert_expires_at, created_at
		 FROM domains WHERE app_id = ? ORDER BY created_at`,
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.AppID, &d.Hostname, &d.IsPrimary, &d.TLSStatus, &d.CertExpiresAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListAllDomains returns every domain across apps (monitor use).
func (s *Store) ListAllDomains() ([]Domain, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, hostname, is_primary, tls_status, cert_expires_at, created_at FROM domains ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.AppID, &d.Hostname, &d.IsPrimary, &d.TLSStatus, &d.CertExpiresAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDomain removes a domain.
func (s *Store) DeleteDomain(id string) error {
	_, err := s.db.Exec(`DELETE FROM domains WHERE id = ?`, id)
	return err
}
