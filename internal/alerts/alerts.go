// Package alerts delivers catalog events to notification targets.
//
// The dispatcher is dumb: emitters (worker, monitor) translate state
// transitions into catalog events and call Notify; dedup and thresholds
// live in the sources. Delivery is best-effort and synchronous (5 s
// timeout, one retry) — notifications must never back-pressure deploys.
package alerts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// Catalog events.
const (
	EventDeployFailed     = "deploy_failed"
	EventDeploySucceeded  = "deploy_succeeded"
	EventUptimeDown       = "uptime_down"
	EventUptimeRecovered  = "uptime_recovered"
	EventCertExpiring     = "cert_expiring"
	EventCertFailed       = "cert_failed"
	EventContainerRestart = "container_restart"
	EventDiskAlmostFull   = "disk_almost_full"
	EventAppUnhealthy     = "app_unhealthy"
	EventAppRecovered     = "app_recovered"
	EventResourceResized  = "resource_resized"
	EventBackupFailed     = "backup_failed"
	EventRestoreFailed    = "restore_failed"
)

// CatalogEvent pairs an event name with its UI label.
type CatalogEvent struct {
	Event string
	Label string
}

// Catalog is the full v1 event list, in UI order.
var Catalog = []CatalogEvent{
	{EventDeployFailed, "Deploy failed"},
	{EventDeploySucceeded, "Deploy succeeded"},
	{EventAppUnhealthy, "App unhealthy"},
	{EventAppRecovered, "App recovered"},
	{EventUptimeDown, "Uptime down"},
	{EventUptimeRecovered, "Uptime recovered"},
	{EventCertExpiring, "Certificate expiring"},
	{EventCertFailed, "Certificate failed"},
	{EventContainerRestart, "Container restarting"},
	{EventDiskAlmostFull, "Disk almost full"},
	{EventResourceResized, "Resource resized"},
	{EventBackupFailed, "Backup failed"},
	{EventRestoreFailed, "Restore failed"},
}

// KnownEvent reports whether a name is in the catalog.
func KnownEvent(event string) bool {
	for _, e := range Catalog {
		if e.Event == event {
			return true
		}
	}
	return false
}

// Dispatcher fans events out to subscribed targets.
type Dispatcher struct {
	store  *store.Store
	encKey [32]byte
	client *http.Client
}

// New builds a Dispatcher.
func New(st *store.Store, encKey [32]byte) *Dispatcher {
	return &Dispatcher{
		store:  st,
		encKey: encKey,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Notify sends an event to every enabled alert subscribed to it. It
// records each delivery attempt for the UI. Blocking is fine: callers are
// worker/monitor goroutines, never HTTP handlers.
func (d *Dispatcher) Notify(event, subject, details string) {
	targets, err := d.store.ListEnabledAlerts(event)
	if err != nil {
		slog.Error("alerts: list targets", "event", event, "err", err)
		return
	}
	for _, t := range targets {
		d.deliver(t, event, subject, details)
	}
}

func (d *Dispatcher) deliver(t store.Alert, event, subject, details string) {
	url, err := crypto.Decrypt(d.encKey, t.URLEnc)
	if err != nil {
		d.record(t.ID, event, subject, false, 0, "decrypt failed")
		return
	}
	payload := slackPayload(event, subject, details)
	code, err := d.post(url, payload)
	if err != nil {
		slog.Warn("alerts: delivery failed (retrying)", "alert", t.ID, "event", event, "err", err)
		code, err = d.post(url, payload)
	}
	if err != nil {
		d.record(t.ID, event, subject, false, code, err.Error())
		return
	}
	d.record(t.ID, event, subject, true, code, "")
}

func (d *Dispatcher) post(url string, payload []byte) (int, error) {
	resp, err := d.client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func (d *Dispatcher) record(alertID, event, subject string, delivered bool, code int, errMsg string) {
	if rerr := d.store.RecordAlertEvent(store.AlertEvent{
		AlertID: alertID, Event: event, Subject: subject, TS: store.Now(),
		Delivered: delivered, ResponseCode: code, Error: errMsg,
	}); rerr != nil {
		slog.Error("alerts: record delivery", "err", rerr)
	}
}

// slackPayload renders the Slack-compatible JSON body.
func slackPayload(event, subject, details string) []byte {
	color := "good"
	switch event {
	case EventDeployFailed, EventUptimeDown, EventCertFailed, EventContainerRestart, EventDiskAlmostFull, EventAppUnhealthy, EventBackupFailed, EventRestoreFailed:
		color = "danger"
	case EventUptimeRecovered, EventAppRecovered, EventCertExpiring:
		color = "warning"
	}
	body, _ := json.Marshal(map[string]any{
		"text": "[deploymate] " + subject,
		"attachments": []map[string]any{
			{"color": color, "title": event, "text": details, "ts": time.Now().Unix()},
		},
	})
	return body
}
