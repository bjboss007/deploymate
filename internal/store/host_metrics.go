package store

import "time"

// HostMetric is one reading of the machine DeployMate runs on.
type HostMetric struct {
	TS    string
	CPU   float64 // percent
	Mem   float64 // percent of memory in use (not counting cache)
	Disk  float64 // percent of the data disk in use
	Load1 float64
	NetRx uint64 // bytes per second
	NetTx uint64
	TempC float64 // -1 when there is no sensor
}

// InsertHostMetric records one reading.
func (s *Store) InsertHostMetric(m HostMetric) error {
	_, err := s.db.Exec(
		`INSERT INTO host_metrics (ts, cpu_pct, mem_pct, disk_pct, load1, net_rx, net_tx, temp_c) VALUES (?,?,?,?,?,?,?,?)`,
		m.TS, m.CPU, m.Mem, m.Disk, m.Load1, m.NetRx, m.NetTx, m.TempC)
	return err
}

// ListHostMetrics returns readings taken at or after since, oldest first.
func (s *Store) ListHostMetrics(since time.Time) ([]HostMetric, error) {
	rows, err := s.db.Query(
		`SELECT ts, cpu_pct, mem_pct, disk_pct, load1, net_rx, net_tx, temp_c FROM host_metrics WHERE ts >= ? ORDER BY ts ASC`,
		since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostMetric
	for rows.Next() {
		var m HostMetric
		if err := rows.Scan(&m.TS, &m.CPU, &m.Mem, &m.Disk, &m.Load1, &m.NetRx, &m.NetTx, &m.TempC); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PruneHostMetricsBefore deletes readings older than the given time.
func (s *Store) PruneHostMetricsBefore(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM host_metrics WHERE ts < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
