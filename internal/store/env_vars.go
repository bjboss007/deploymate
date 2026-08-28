package store

// EnvVar is one environment variable on an app. Every value is encrypted at
// rest; IsSecret only controls how the UI displays it.
type EnvVar struct {
	ID        string
	AppID     string
	Key       string
	ValueEnc  string
	Value     string // decrypted form, filled by the caller when needed
	IsSecret  bool
	CreatedAt string
}

// UpsertEnvVar inserts or replaces a variable for (app, key).
func (s *Store) UpsertEnvVar(v EnvVar) (EnvVar, error) {
	v.ID = NewID()
	v.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO env_vars (id, app_id, key, value_enc, is_secret, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(app_id, key) DO UPDATE SET value_enc = excluded.value_enc, is_secret = excluded.is_secret`,
		v.ID, v.AppID, v.Key, v.ValueEnc, v.IsSecret, v.CreatedAt,
	)
	return v, err
}

// ListEnvVars returns all variables for an app, ordered by key.
func (s *Store) ListEnvVars(appID string) ([]EnvVar, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, key, value_enc, is_secret, created_at FROM env_vars WHERE app_id = ? ORDER BY key`,
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EnvVar
	for rows.Next() {
		var v EnvVar
		if err := rows.Scan(&v.ID, &v.AppID, &v.Key, &v.ValueEnc, &v.IsSecret, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteEnvVar removes a variable by ID.
func (s *Store) DeleteEnvVar(id string) error {
	_, err := s.db.Exec(`DELETE FROM env_vars WHERE id = ?`, id)
	return err
}
