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

// P90Metrics returns the P90 memory and CPU usage over the window, plus
// the sample count. samples < 10 means "not enough data" — callers should
// not derive limits from the returned values.
func (s *Store) P90Metrics(appID string, since time.Time) (memP90 uint64, cpuP90 float64, samples int, err error) {
	var count int
	if err = s.db.QueryRow(
		`SELECT COUNT(*) FROM metrics WHERE app_id = ? AND ts >= ?`,
		appID, since.UTC().Format(time.RFC3339Nano),
	).Scan(&count); err != nil {
		return 0, 0, 0, err
	}
	if count < 10 {
		return 0, 0, count, nil
	}
	offset := count*9/10 - 1 // 0-indexed position of the 90th percentile
	var mem uint64
	if err = s.db.QueryRow(
		`SELECT mem_bytes FROM metrics WHERE app_id = ? AND ts >= ? ORDER BY mem_bytes ASC LIMIT 1 OFFSET ?`,
		appID, since.UTC().Format(time.RFC3339Nano), offset,
	).Scan(&mem); err != nil {
		return 0, 0, count, err
	}
	var cpu float64
	if err = s.db.QueryRow(
		`SELECT cpu_pct FROM metrics WHERE app_id = ? AND ts >= ? ORDER BY cpu_pct ASC LIMIT 1 OFFSET ?`,
		appID, since.UTC().Format(time.RFC3339Nano), offset,
	).Scan(&cpu); err != nil {
		return 0, 0, count, err
	}
	return mem, cpu, count, nil
}

// PruneMetricsBefore deletes samples older than the given time.
func (s *Store) PruneMetricsBefore(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM metrics WHERE ts < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
