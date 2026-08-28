// Package monitor samples container resources and probes domain uptime on
// fixed cadences, writing to the metadata DB with bounded retention.
package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

const (
	statsInterval  = 5 * time.Second
	probeInterval  = 30 * time.Second
	pruneInterval  = time.Hour
	metricsRetain  = 7 * 24 * time.Hour
	uptimeRetain   = 30 * 24 * time.Hour
	probeTimeout   = 5 * time.Second
)

// Monitor runs the stats and uptime loops.
type Monitor struct {
	store *store.Store
	rt    runtime.Runtime
	http  *http.Client
}

// New builds a Monitor. The HTTP client skips TLS verification so staging
// Let's Encrypt certificates still probe as healthy.
func New(st *store.Store, rt runtime.Runtime) *Monitor {
	return &Monitor{
		store: st,
		rt:    rt,
		http: &http.Client{
			Timeout: probeTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// Run loops until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	slog.Info("monitor started")
	statsT := time.NewTicker(statsInterval)
	probeT := time.NewTicker(probeInterval)
	pruneT := time.NewTicker(pruneInterval)
	defer statsT.Stop()
	defer probeT.Stop()
	defer pruneT.Stop()

	// First pass right away so the UI has data immediately.
	m.sampleAll(ctx)
	m.probeAll(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("monitor stopped")
			return
		case <-statsT.C:
			m.sampleAll(ctx)
		case <-probeT.C:
			m.probeAll(ctx)
		case <-pruneT.C:
			m.prune()
		}
	}
}

// sampleAll records one stats sample per running app.
func (m *Monitor) sampleAll(ctx context.Context) {
	apps, err := m.store.ListAllApps()
	if err != nil {
		slog.Error("monitor: list apps", "err", err)
		return
	}
	for _, app := range apps {
		if app.Status != "running" {
			continue
		}
		stats, err := m.rt.Stats(ctx, "dm-"+app.Slug)
		if err != nil {
			// Container restarted or vanished between list and sample —
			// not an error worth logging every 5s.
			continue
		}
		if err := m.store.InsertMetric(app.ID, store.Metric{
			TS:        store.Now(),
			CPUPercent: stats.CPUPercent,
			MemBytes:  stats.MemBytes,
			NetRx:     stats.NetRx,
			NetTx:     stats.NetTx,
		}); err != nil {
			slog.Error("monitor: insert metric", "err", err)
		}
	}
}

// probeAll checks every domain over HTTPS and records the result.
func (m *Monitor) probeAll(ctx context.Context) {
	domains, err := m.store.ListAllDomains()
	if err != nil {
		slog.Error("monitor: list domains", "err", err)
		return
	}
	for _, d := range domains {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+d.Hostname, nil)
		if err != nil {
			continue
		}
		resp, err := m.http.Do(req)
		latency := time.Since(start).Milliseconds()
		check := store.UptimeCheck{TS: store.Now(), LatencyMS: latency}
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				check.OK = false
				_ = m.store.InsertUptimeCheck(d.ID, check)
			}
			continue
		}
		check.StatusCode = resp.StatusCode
		resp.Body.Close()
		check.OK = resp.StatusCode < 500
		if err := m.store.InsertUptimeCheck(d.ID, check); err != nil {
			slog.Error("monitor: insert uptime", "err", err)
		}
	}
}

func (m *Monitor) prune() {
	now := time.Now().UTC()
	if n, err := m.store.PruneMetricsBefore(now.Add(-metricsRetain)); err != nil {
		slog.Error("monitor: prune metrics", "err", err)
	} else if n > 0 {
		slog.Info("monitor: pruned metrics", "count", n)
	}
	if n, err := m.store.PruneUptimeBefore(now.Add(-uptimeRetain)); err != nil {
		slog.Error("monitor: prune uptime", "err", err)
	} else if n > 0 {
		slog.Info("monitor: pruned uptime checks", "count", n)
	}
}
