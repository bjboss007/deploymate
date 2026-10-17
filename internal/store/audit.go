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

// ListAudit returns the newest token-driven entries first (dashboard sign-ins have their own list).
func (s *Store) ListAudit(limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, ts, token_id, token_name, action, target, detail, result FROM audit_log WHERE action <> 'login' ORDER BY id DESC LIMIT ?`, limit)
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

// Dashboard sign-ins live in audit_log as action "login": target = email, detail = client address,
// result = ok | refused (wrong password) | blocked (an address or email hit the limit).

// LoginAttempt is one dashboard sign-in attempt. NewAddress marks a successful sign-in from an address that
// never signed in successfully before.
type LoginAttempt struct {
	TS, Email, Address, Result string
	NewAddress                 bool
}

// ListLogins returns the newest sign-in attempts first.
func (s *Store) ListLogins(limit int) ([]LoginAttempt, error) {
	rows, err := s.db.Query(`SELECT id, ts, target, detail, result FROM audit_log WHERE action = 'login' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		id int64
		LoginAttempt
	}
	var tmp []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.TS, &r.Email, &r.Address, &r.Result); err != nil {
			return nil, err
		}
		tmp = append(tmp, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]LoginAttempt, 0, len(tmp))
	for _, r := range tmp {
		if r.Result == "ok" && r.Address != "" {
			var n int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'login' AND result = 'ok' AND detail = ? AND id < ?`, r.Address, r.id).Scan(&n); err == nil {
				r.NewAddress = n == 0
			}
		}
		out = append(out, r.LoginAttempt)
	}
	return out, nil
}

// LoginCounts counts sign-in attempts by result since a timestamp (RFC3339, UTC).
func (s *Store) LoginCounts(since string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT result, COUNT(*) FROM audit_log WHERE action = 'login' AND ts >= ? GROUP BY result`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var res string
		var n int
		if err := rows.Scan(&res, &n); err != nil {
			return nil, err
		}
		out[res] = n
	}
	return out, rows.Err()
}

// PruneLogins deletes sign-in records older than a timestamp, so a scan cannot grow the table forever.
func (s *Store) PruneLogins(before string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM audit_log WHERE action = 'login' AND ts < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
