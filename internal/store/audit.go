package store

// AuditEntry is one state-changing API call.
type AuditEntry struct {
	ID        int64
	TS        string
	TokenID   string
	TokenName string
	Action    string
	Target    string
	Detail    string
	Result    string // ok | refused
}

// RecordAudit appends an entry.
func (s *Store) RecordAudit(e AuditEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO audit_log (ts, token_id, token_name, action, target, detail, result) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		Now(), e.TokenID, e.TokenName, e.Action, e.Target, e.Detail, e.Result)
	return err
}

// ListAudit returns the newest entries first.
func (s *Store) ListAudit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, ts, token_id, token_name, action, target, detail, result FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.TokenID, &e.TokenName, &e.Action, &e.Target, &e.Detail, &e.Result); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
