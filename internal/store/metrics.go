package store

import "time"

// Metric is one resource sample for an app — one replica slot's sample, or
// (from ListMetrics) the total across replicas at one sampling tick.
type Metric struct {
	Slot      int // replica slot (0 = 1 on insert)
	TS        string
	CPUPercent float64
	MemBytes  uint64
	NetRx     uint64
	NetTx     uint64
}

// InsertMetric records one replica's sample. The monitor stamps every
// slot sampled in one tick with the same TS, which is what lets
// ListMetrics total them.
func (s *Store) InsertMetric(appID string, m Metric) error {
	slot := m.Slot
	if slot < 1 {
		slot = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO metrics (app_id, slot, ts, cpu_pct, mem_bytes, net_rx, net_tx) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		appID, slot, m.TS, m.CPUPercent, m.MemBytes, m.NetRx, m.NetTx,
	)
	return err
}

// ListMetrics returns the app's newest `limit` sampling ticks, oldest
// first, each the TOTAL across replicas (the app's whole footprint).
func (s *Store) ListMetrics(appID string, limit int) ([]Metric, error) {
	return s.listMetrics(
		`SELECT ts, SUM(cpu_pct), SUM(mem_bytes), SUM(net_rx), SUM(net_tx) FROM metrics
		 WHERE app_id = ? GROUP BY ts ORDER BY MAX(id) DESC LIMIT ?`,
		appID, limit)
}

// ListSlotMetrics returns one replica's newest `limit` samples, oldest first.
func (s *Store) ListSlotMetrics(appID string, slot, limit int) ([]Metric, error) {
	return s.listMetrics(
		`SELECT ts, cpu_pct, mem_bytes, net_rx, net_tx FROM metrics
		 WHERE app_id = ? AND slot = ? ORDER BY id DESC LIMIT ?`,
		appID, slot, limit)
}

// listMetrics scans newest-first rows and returns them oldest first.
func (s *Store) listMetrics(query string, args ...any) ([]Metric, error) {
	rows, err := s.db.Query(query, args...)
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
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// P90Metrics returns the P90 memory and CPU usage over the window across
// every replica's samples (a per-container distribution), plus the sample
// count. samples < 10 means "not enough data" — callers should not derive
// limits from the returned values.
func (s *Store) P90Metrics(appID string, since time.Time) (memP90 uint64, cpuP90 float64, samples int, err error) {
	return s.P90SlotMetrics(appID, 0, since)
}

// P90SlotMetrics is P90Metrics for one replica slot; slot 0 = every slot.
func (s *Store) P90SlotMetrics(appID string, slot int, since time.Time) (memP90 uint64, cpuP90 float64, samples int, err error) {
	where, args := `app_id = ? AND ts >= ?`, []any{appID, since.UTC().Format(time.RFC3339Nano)}
	if slot > 0 {
		where += ` AND slot = ?`
		args = append(args, slot)
	}
	var count int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM metrics WHERE `+where, args...).Scan(&count); err != nil {
		return 0, 0, 0, err
	}
	if count < 10 {
		return 0, 0, count, nil
	}
	offset := count*9/10 - 1 // 0-indexed position of the 90th percentile
	var mem uint64
	if err = s.db.QueryRow(
		`SELECT mem_bytes FROM metrics WHERE `+where+` ORDER BY mem_bytes ASC LIMIT 1 OFFSET ?`,
		append(args, offset)...,
	).Scan(&mem); err != nil {
		return 0, 0, count, err
	}
	var cpu float64
	if err = s.db.QueryRow(
		`SELECT cpu_pct FROM metrics WHERE `+where+` ORDER BY cpu_pct ASC LIMIT 1 OFFSET ?`,
		append(args, offset)...,
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
