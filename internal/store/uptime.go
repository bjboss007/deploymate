package store

import "time"

// UptimeCheck is one probe result for a domain.
type UptimeCheck struct {
	TS         string
	OK         bool
	StatusCode int
	LatencyMS  int64
}

// InsertUptimeCheck records one probe.
func (s *Store) InsertUptimeCheck(domainID string, u UptimeCheck) error {
	ok := 0
	if u.OK {
		ok = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO uptime_checks (domain_id, ts, ok, status_code, latency_ms) VALUES (?, ?, ?, ?, ?)`,
		domainID, u.TS, ok, u.StatusCode, u.LatencyMS,
	)
	return err
}

// ListUptimeChecks returns the newest `limit` checks for a domain, oldest
// first.
func (s *Store) ListUptimeChecks(domainID string, limit int) ([]UptimeCheck, error) {
	rows, err := s.db.Query(
		`SELECT ts, ok, status_code, latency_ms FROM
		   (SELECT id, ts, ok, status_code, latency_ms FROM uptime_checks WHERE domain_id = ? ORDER BY id DESC LIMIT ?)
		 ORDER BY id`,
		domainID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UptimeCheck
	for rows.Next() {
		var u UptimeCheck
		var ok int
		if err := rows.Scan(&u.TS, &ok, &u.StatusCode, &u.LatencyMS); err != nil {
			return nil, err
		}
		u.OK = ok == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

// PruneUptimeBefore deletes checks older than the given time.
func (s *Store) PruneUptimeBefore(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM uptime_checks WHERE ts < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
