package store

import (
	"encoding/json"
	"strings"
)

// Alert is one notification target: a channel endpoint (encrypted at
// rest) subscribed to a set of catalog events.
type Alert struct {
	ID        string
	Name      string
	Channel   string
	URLEnc    string
	Enabled   bool
	Events    []string
	CreatedAt string
}

// AlertEvent is one delivery attempt record.
type AlertEvent struct {
	ID           int64
	AlertID      string
	Event        string
	Subject      string
	TS           string
	Delivered    bool
	ResponseCode int
	Error        string
}

// CreateAlert inserts a new alert target.
func (s *Store) CreateAlert(a Alert) (Alert, error) {
	a.ID = NewID()
	a.CreatedAt = Now()
	events, err := json.Marshal(a.Events)
	if err != nil {
		return a, err
	}
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err = s.db.Exec(
		`INSERT INTO alerts (id, name, channel, url_enc, enabled, events, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Name, a.Channel, a.URLEnc, enabled, string(events), a.CreatedAt,
	)
	return a, err
}

// ListAlerts returns all alert targets, newest first.
func (s *Store) ListAlerts() ([]Alert, error) {
	rows, err := s.db.Query(
		`SELECT id, name, channel, url_enc, enabled, events, created_at FROM alerts ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		var enabled int
		var eventsJSON string
		if err := rows.Scan(&a.ID, &a.Name, &a.Channel, &a.URLEnc, &enabled, &eventsJSON, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Enabled = enabled == 1
		_ = json.Unmarshal([]byte(eventsJSON), &a.Events)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListEnabledAlerts returns enabled targets subscribed to the given event.
func (s *Store) ListEnabledAlerts(event string) ([]Alert, error) {
	alerts, err := s.ListAlerts()
	if err != nil {
		return nil, err
	}
	var out []Alert
	for _, a := range alerts {
		if !a.Enabled {
			continue
		}
		for _, e := range a.Events {
			if e == event {
				out = append(out, a)
				break
			}
		}
	}
	return out, nil
}

// DeleteAlert removes a target (its delivery history cascades).
func (s *Store) DeleteAlert(id string) error {
	_, err := s.db.Exec(`DELETE FROM alerts WHERE id = ?`, id)
	return err
}

// RecordAlertEvent appends one delivery attempt.
func (s *Store) RecordAlertEvent(ev AlertEvent) error {
	delivered := 0
	if ev.Delivered {
		delivered = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO alert_events (alert_id, event, subject, ts, delivered, response_code, error) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.AlertID, ev.Event, ev.Subject, ev.TS, delivered, ev.ResponseCode, truncate(ev.Error, 500),
	)
	return err
}

// ListAlertEvents returns the newest deliveries for an alert.
func (s *Store) ListAlertEvents(alertID string, limit int) ([]AlertEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, alert_id, event, subject, ts, delivered, response_code, error FROM
		   (SELECT id, alert_id, event, subject, ts, delivered, response_code, error FROM alert_events WHERE alert_id = ? ORDER BY id DESC LIMIT ?)
		 ORDER BY id DESC`,
		alertID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertEvent
	for rows.Next() {
		var ev AlertEvent
		var delivered int
		if err := rows.Scan(&ev.ID, &ev.AlertID, &ev.Event, &ev.Subject, &ev.TS, &delivered, &ev.ResponseCode, &ev.Error); err != nil {
			return nil, err
		}
		ev.Delivered = delivered == 1
		out = append(out, ev)
	}
	return out, rows.Err()
}

// PruneAlertEventsBefore deletes deliveries older than ts. Returns count.
func (s *Store) PruneAlertEventsBefore(ts string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM alert_events WHERE ts < ?`, ts)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}
