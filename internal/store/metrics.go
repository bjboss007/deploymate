package store

import "time"

// Metric is one resource sample for an app.
type Metric struct {
	TS        string
	CPUPercent float64
	MemBytes  uint64
	NetRx     uint64
	NetTx     uint64
}

// InsertMetric records one sample.
func (s *Store) InsertMetric(appID string, m Metric) error {
	_, err := s.db.Exec(
		`INSERT INTO metrics (app_id, ts, cpu_pct, mem_bytes, net_rx, net_tx) VALUES (?, ?, ?, ?, ?, ?)`,
		appID, m.TS, m.CPUPercent, m.MemBytes, m.NetRx, m.NetTx,
	)
	return err
}

// ListMetrics returns the newest `limit` samples for an app, oldest first.
func (s *Store) ListMetrics(appID string, limit int) ([]Metric, error) {
	rows, err := s.db.Query(
		`SELECT ts, cpu_pct, mem_bytes, net_rx, net_tx FROM
		   (SELECT id, ts, cpu_pct, mem_bytes, net_rx, net_tx FROM metrics WHERE app_id = ? ORDER BY id DESC LIMIT ?)
		 ORDER BY id`,
		appID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Metric
	for rows.Next() {
		var m Metric
		if err := rows.Scan(&m.TS, &m.CPUPercent, &m.MemBytes, &m.NetRx, &m.NetTx); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneMetricsBefore deletes samples older than the given time.
func (s *Store) PruneMetricsBefore(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM metrics WHERE ts < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
