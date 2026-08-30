package store

import "time"

// AppendBuildLog adds one build output line to a deployment's log, assigning
// the next sequence number. Returns the assigned seq.
func (s *Store) AppendBuildLog(deploymentID, stream, line string) error {
	var seq int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM build_logs WHERE deployment_id = ?`,
		deploymentID,
	).Scan(&seq); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO build_logs (deployment_id, seq, stream, line, ts) VALUES (?, ?, ?, ?, ?)`,
		deploymentID, seq, stream, line, Now(),
	)
	return err
}

// ListBuildLogs returns a deployment's log lines after afterSeq, oldest first.
func (s *Store) ListBuildLogs(deploymentID string, afterSeq int) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT line FROM build_logs WHERE deployment_id = ? AND seq > ? ORDER BY seq`,
		deploymentID, afterSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// PruneBuildLogsBefore deletes build-log lines older than the given time.
// Build logs accumulate forever otherwise — one row per output line across
// every deployment. The monitor calls this in its hourly pass (same as
// metrics/uptime), keeping a bounded window. The deployment rows themselves
// are small and stay; only their verbose line-by-line output is pruned.
func (s *Store) PruneBuildLogsBefore(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM build_logs WHERE ts < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
