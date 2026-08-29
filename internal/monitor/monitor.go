// Package monitor samples container resources, probes app health and
// domain uptime, watches restart counts and disk usage — and translates
// every meaningful transition into alert events.
package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

const (
	statsInterval = 5 * time.Second
	probeInterval = 30 * time.Second
	pruneInterval = time.Hour
	metricsRetain = 7 * 24 * time.Hour
	uptimeRetain  = 30 * 24 * time.Hour
	probeTimeout  = 5 * time.Second

	// Health hysteresis: 3 consecutive failed probes = unhealthy,
	// 2 consecutive successes = recovered. Prevents flap spam.
	unhealthyAfterFails = 3
	recoveredAfterOKs   = 2

	diskThreshold   = 20 << 30 // 20 GiB of images + build cache
	diskAlertRepeat = 24 * time.Hour
	restartCooldown = 5 * time.Minute
)

// Monitor runs the stats, health, uptime, restart, and disk loops.
type Monitor struct {
	store  *store.Store
	rt     runtime.Runtime
	alerts *alerts.Dispatcher
	http   *http.Client

	mu             sync.Mutex
	uptimeState    map[string]bool   // domainID -> last probe ok
	healthFails    map[string]int    // appID -> consecutive failed probes
	restartSeen    map[string]int    // appID -> last RestartCount
	restartAlertAt map[string]time.Time
	lastDiskAlert  time.Time
}

// New builds a Monitor. The HTTP client skips TLS verification so staging
// Let's Encrypt certificates still probe as healthy.
func New(st *store.Store, rt runtime.Runtime, a *alerts.Dispatcher) *Monitor {
	return &Monitor{
		store: st, rt: rt, alerts: a,
		http: &http.Client{
			Timeout: probeTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		uptimeState:    make(map[string]bool),
		healthFails:    make(map[string]int),
		restartSeen:    make(map[string]int),
		restartAlertAt: make(map[string]time.Time),
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
	m.probeApps(ctx)
	m.checkDisk(ctx)

	for {
		select {
		case <-ctx.Done():
			slog.Info("monitor stopped")
			return
		case <-statsT.C:
			m.sampleAll(ctx)
		case <-probeT.C:
			m.probeAll(ctx)
			m.probeApps(ctx)
			m.checkDisk(ctx)
		case <-pruneT.C:
			m.prune()
		}
	}
}

// probeApps runs per-app health probes and restart-count checks.
func (m *Monitor) probeApps(ctx context.Context) {
	apps, err := m.store.ListAllApps()
	if err != nil {
		slog.Error("monitor: list apps for health", "err", err)
		return
	}
	for _, app := range apps {
		m.probeAppHealth(ctx, app)
		m.checkRestarts(ctx, app)
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

// probeAll checks every domain over HTTPS and records the result,
// alerting on ok↔not-ok transitions only.
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
		ok := false
		if err == nil {
			check.StatusCode = resp.StatusCode
			resp.Body.Close()
			ok = resp.StatusCode < 500
		}
		check.OK = ok
		if err != nil && !errors.Is(err, context.Canceled) {
			_ = m.store.InsertUptimeCheck(d.ID, check)
		} else if err == nil {
			_ = m.store.InsertUptimeCheck(d.ID, check)
		}

		m.mu.Lock()
		prev, seen := m.uptimeState[d.ID]
		if seen && prev != ok {
			if ok {
				m.alerts.Notify(alerts.EventUptimeRecovered,
					fmt.Sprintf("uptime recovered: %s", d.Hostname),
					fmt.Sprintf("https://%s is responding again (%d in %d ms)", d.Hostname, check.StatusCode, latency))
			} else {
				m.alerts.Notify(alerts.EventUptimeDown,
					fmt.Sprintf("uptime down: %s", d.Hostname),
					fmt.Sprintf("https://%s failed (status %d, %d ms)", d.Hostname, check.StatusCode, latency))
			}
		}
		m.uptimeState[d.ID] = ok
		m.mu.Unlock()
	}
}

// probeAppHealth probes every running app with a port over its loopback
// preview binding, maintaining apps.health with hysteresis and alerting
// on unhealthy/recovered transitions.
func (m *Monitor) probeAppHealth(ctx context.Context, app store.App) {
	if app.Status != "running" || app.Port <= 0 {
		return
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", runtime.PreviewPort(app.Slug))
	resp, err := m.http.Get(url)
	ok := err == nil && resp != nil && resp.StatusCode < 500
	if resp != nil {
		resp.Body.Close()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		if app.Health == "unhealthy" {
			m.healthFails[app.ID]++
			if m.healthFails[app.ID] >= recoveredAfterOKs {
				_ = m.store.UpdateAppHealth(app.ID, "healthy")
				delete(m.healthFails, app.ID)
				m.alerts.Notify(alerts.EventAppRecovered,
					fmt.Sprintf("app recovered: %s", app.Name),
					fmt.Sprintf("%s is healthy again on port %d", app.Name, app.Port))
			}
			return
		}
		delete(m.healthFails, app.ID)
		if app.Health != "healthy" {
			_ = m.store.UpdateAppHealth(app.ID, "healthy")
		}
		return
	}

	m.healthFails[app.ID]++
	if m.healthFails[app.ID] >= unhealthyAfterFails && app.Health != "unhealthy" {
		_ = m.store.UpdateAppHealth(app.ID, "unhealthy")
		m.alerts.Notify(alerts.EventAppUnhealthy,
			fmt.Sprintf("app unhealthy: %s", app.Name),
			fmt.Sprintf("%s failed %d health probes on port %d — it may be crash-looping", app.Name, unhealthyAfterFails, app.Port))
	}
}

// checkRestarts alerts when a running app's container restart count climbs.
func (m *Monitor) checkRestarts(ctx context.Context, app store.App) {
	info, err := m.rt.Inspect(ctx, "dm-"+app.Slug)
	if err != nil {
		return
	}
	restarts := info.Restarts
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, seen := m.restartSeen[app.ID]; seen && restarts > prev {
		if last, ok := m.restartAlertAt[app.ID]; !ok || time.Since(last) > restartCooldown {
			m.restartAlertAt[app.ID] = time.Now()
			m.alerts.Notify(alerts.EventContainerRestart,
				fmt.Sprintf("container restarting: %s", app.Name),
				fmt.Sprintf("%s restarted (count %d) — check its logs", app.Name, restarts))
		}
	}
	m.restartSeen[app.ID] = restarts
}

// checkDisk alerts once per day when docker's growable storage (images +
// build cache) crosses the threshold — the failure mode that bit us on
// Docker Desktop, where the daemon cannot report host-disk free space.
func (m *Monitor) checkDisk(ctx context.Context) {
	used, err := m.rt.StorageUsed(ctx)
	if err != nil || used < diskThreshold {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.lastDiskAlert) < diskAlertRepeat {
		return
	}
	m.lastDiskAlert = time.Now()
	m.alerts.Notify(alerts.EventDiskAlmostFull,
		"docker storage is large",
		fmt.Sprintf("%.1f GB of images + build cache — prune with `docker system prune` and `docker exec dm-buildkit buildctl prune`",
			float64(used)/(1<<30)))
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
	if n, err := m.store.PruneAlertEventsBefore(now.Add(-uptimeRetain).Format(time.RFC3339Nano)); err != nil {
		slog.Error("monitor: prune alert events", "err", err)
	} else if n > 0 {
		slog.Info("monitor: pruned alert deliveries", "count", n)
	}
}
