package store

// ListAppServiceExclusions returns the ids of the services an app has opted
// out of receiving (the connection-URL injection skips them).
func (s *Store) ListAppServiceExclusions(appID string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT service_id FROM app_service_exclusions WHERE app_id = ?`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SetAppServiceExcluded opts an app out of (or back into) a service's
// connection URL. Idempotent.
func (s *Store) SetAppServiceExcluded(appID, serviceID string, excluded bool) error {
	if excluded {
		_, err := s.db.Exec(
			`INSERT OR IGNORE INTO app_service_exclusions (app_id, service_id, created_at) VALUES (?, ?, ?)`,
			appID, serviceID, Now())
		return err
	}
	_, err := s.db.Exec(`DELETE FROM app_service_exclusions WHERE app_id = ? AND service_id = ?`, appID, serviceID)
	return err
}
