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
	statsInterval   = 5 * time.Second
	probeInterval   = 30 * time.Second
	pruneInterval   = time.Hour
	metricsRetain   = 7 * 24 * time.Hour
	uptimeRetain    = 30 * 24 * time.Hour
	buildLogsRetain = 30 * 24 * time.Hour
	probeTimeout    = 5 * time.Second

	// Health hysteresis: 3 consecutive failed probes = unhealthy,
	// 2 consecutive successes = recovered. Prevents flap spam.
	unhealthyAfterFails = 3
	recoveredAfterOKs   = 2

	diskThreshold   = 20 << 30 // 20 GiB of images + build cache
	diskAlertRepeat = 24 * time.Hour
	restartCooldown = 5 * time.Minute
	healCooldown    = 5 * time.Minute // between auto-heal attempts per app

	// Auto-resize: sustained usage past this fraction of the APPLIED limit
	// triggers a limit bump + automatic redeploy (docker requires a
	// recreate to apply new limits).
	resizePressure = 0.80
	resizeCooldown = 30 * time.Minute
	pressureWindow = 10 * time.Minute
)

// Monitor runs the stats, health, uptime, restart, and disk loops.
type Monitor struct {
	store  *store.Store
	rt     runtime.Runtime
	alerts *alerts.Dispatcher
	http   *http.Client
	// heal recreates a bindingless app container from its spec; wired by
	// the server via SetHealer so the recreate matches a deploy exactly.
	heal func(ctx context.Context, app store.App) (bool, error)
	// probeURLFn overrides the loopback probe target per slug; tests use
	// it to point at a port that is always closed (the default preview
	// port can be occupied by a real container on the host).
	probeURLFn func(slug string) string

	mu             sync.Mutex
	uptimeState    map[string]bool   // domainID -> last probe ok
	healthFails    map[string]int    // appID -> consecutive failed probes
	lastHeal       map[string]time.Time // appID -> last auto-heal attempt
	restartSeen    map[string]int    // appID -> last RestartCount
	restartAlertAt map[string]time.Time
	lastResize     map[string]time.Time
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
		lastHeal:       make(map[string]time.Time),
		restartSeen:    make(map[string]int),
		restartAlertAt: make(map[string]time.Time),
		lastResize:     make(map[string]time.Time),
	}
}

// SetHealer wires the app heal callback used to recreate bindingless
// containers; called once at startup before Run.
func (m *Monitor) SetHealer(fn func(ctx context.Context, app store.App) (bool, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heal = fn
}

// healDue reports whether an auto-heal attempt is allowed and reserves
// one: attempts are stamped so a heal that cannot succeed (e.g. docker is
// mid-restart) retries at most once per healCooldown instead of every
// probe tick.
func (m *Monitor) healDue(appID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if last, ok := m.lastHeal[appID]; ok && time.Since(last) < healCooldown {
		return false
	}
	m.lastHeal[appID] = time.Now()
	return true
}

// setHealth updates apps.health, logging (not swallowing) store errors so
// the health trail cannot silently desync from the alert trail.
func (m *Monitor) setHealth(id, health string) {
	if err := m.store.UpdateAppHealth(id, health); err != nil {
		slog.Error("monitor: update app health", "app", id, "health", health, "err", err)
	}
}

// recordEvent logs (not swallows) a failed event write.
func (m *Monitor) recordEvent(appID, kind, data string) {
	if err := m.store.RecordEvent(appID, kind, data); err != nil {
		slog.Error("monitor: record event", "kind", kind, "err", err)
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
	m.detectResources(ctx)

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
			m.detectResources(ctx)
		}
	}
}

// probeApps runs per-app health probes, restart checks, and resource
// pressure checks.
func (m *Monitor) probeApps(ctx context.Context) {
	apps, err := m.store.ListAllApps()
	if err != nil {
		slog.Error("monitor: list apps for health", "err", err)
		return
	}
	for _, app := range apps {
		m.probeAppHealth(ctx, app)
		m.checkRestarts(ctx, app)
		m.checkResourcePressure(ctx, app)
	}
}

// checkResourcePressure is the self-healing half of resource detection:
// when recent usage sustains past 80% of the APPLIED limit, bump the
// limit and redeploy automatically (same image, no build — seconds of
// swap). Cooldown prevents thrash loops.
func (m *Monitor) checkResourcePressure(ctx context.Context, app store.App) {
	if app.Status != "running" {
		return
	}
	info, err := m.rt.Inspect(ctx, "dm-"+app.Slug)
	if err != nil || info.MemLimitMB == 0 && info.CPULimit == 0 {
		return // no applied limits — nothing to resize against
	}

	memP90, cpuP90, samples, err := m.store.P90Metrics(app.ID, time.Now().UTC().Add(-pressureWindow))
	if err != nil || samples < 10 {
		return
	}

	memPressure := info.MemLimitMB > 0 && float64(memP90) > resizePressure*float64(info.MemLimitMB<<20)
	cpuPressure := info.CPULimit > 0 && cpuP90 > resizePressure*info.CPULimit*100
	if !memPressure && !cpuPressure {
		return
	}

	// Cooldown lives in the deployments table (not memory) so it survives
	// restarts: skip if a resize ran for this app within the window.
	recent, err := m.store.LatestDeploymentOfKind(app.ID, "resize")
	if err == nil && recent != nil && time.Since(recent.CreatedTime()) < resizeCooldown {
		return
	}

	// New limit: re-derive from recent usage with headroom, never smaller
	// than what's applied, always clamped.
	newMem := clamp64(int64(memP90)*2/(1<<20), 64, 4096)
	if newMem <= info.MemLimitMB {
		newMem = clamp64(info.MemLimitMB*2, 64, 4096)
	}
	newCPU := clampF(cpuP90*2/100, 0.5, 4.0)
	if newCPU <= info.CPULimit {
		newCPU = clampF(info.CPULimit*2, 0.5, 4.0)
	}
	if err := m.store.UpdateAppResources(app.ID, int(newMem), newCPU); err != nil {
		slog.Error("monitor: resize update", "app", app.Slug, "err", err)
		return
	}

	// Redeploy with the current image — the worker's resize path skips
	// the build and just swaps the container with the new limits.
	d, err := m.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "resize", Status: "queued", ImageTag: info.Image,
	})
	if err != nil {
		slog.Error("monitor: queue resize", "app", app.Slug, "err", err)
		return
	}
	_ = m.store.RecordEvent(app.ID, store.EventResourceResized,
		fmt.Sprintf("usage passed 80%% of limit — resized to %d MB / %g CPU, redeploying (%s)", newMem, newCPU, d.ID[:8]))
	m.alerts.Notify(alerts.EventResourceResized,
		fmt.Sprintf("resource resized: %s", app.Name),
		fmt.Sprintf("usage sustained past 80%% of its limit — raised to %d MB / %g CPU and redeploying (%s)",
			newMem, newCPU, d.ID[:8]))
	slog.Info("monitor: resizing app", "app", app.Slug, "mem_mb", newMem, "cpu", newCPU, "deployment", d.ID)
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
	if m.probeURLFn != nil {
		url = m.probeURLFn(app.Slug)
	}
	resp, err := m.http.Get(url)
	ok := err == nil && resp != nil && resp.StatusCode < 500
	if resp != nil {
		resp.Body.Close()
	}

	// Auto-heal: a running app whose container lost its preview port
	// binding (leftovers from old binaries, manual docker runs) fails
	// every probe until the container is recreated. Delegate to the
	// server's spec builder so the recreate matches a deploy exactly;
	// the heal reports whether a recreation actually happened.
	if !ok && m.heal != nil && m.healDue(app.ID) {
		healed, healErr := m.heal(ctx, app)
		if healErr != nil {
			slog.Warn("monitor: auto-heal failed", "app", app.Slug, "err", healErr)
		} else if healed {
			m.setHealth(app.ID, "healthy")
			m.recordEvent(app.ID, store.EventAppHealed,
				"recreated container missing its preview port binding")
			m.alerts.Notify(alerts.EventAppRecovered,
				fmt.Sprintf("app auto-healed: %s", app.Name),
				fmt.Sprintf("recreated %s's container to restore its preview port binding", app.Name))
			return
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		if app.Health == "unhealthy" {
			m.healthFails[app.ID]++
			if m.healthFails[app.ID] >= recoveredAfterOKs {
				m.setHealth(app.ID, "healthy")
				delete(m.healthFails, app.ID)
				m.recordEvent(app.ID, store.EventHealthRecovered,
					fmt.Sprintf("%s responded to %d consecutive health probes", app.Name, recoveredAfterOKs))
				m.alerts.Notify(alerts.EventAppRecovered,
					fmt.Sprintf("app recovered: %s", app.Name),
					fmt.Sprintf("%s is healthy again on port %d", app.Name, app.Port))
			}
			return
		}
		delete(m.healthFails, app.ID)
		if app.Health != "healthy" {
			m.setHealth(app.ID, "healthy")
		}
		return
	}

	m.healthFails[app.ID]++
	if m.healthFails[app.ID] >= unhealthyAfterFails && app.Health != "unhealthy" {
		m.setHealth(app.ID, "unhealthy")
		m.recordEvent(app.ID, store.EventHealthUnhealthy,
			fmt.Sprintf("%d consecutive health probes failed on port %d", unhealthyAfterFails, app.Port))
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

// detectResources derives per-app resource limits from observed usage:
// P90 memory and CPU over the last 24h, doubled for headroom, floored and
// capped to sane bounds. Limits apply at the NEXT deploy (docker requires
// a recreate to change them) — detection is continuous, application is
// per-deploy.
func (m *Monitor) detectResources(ctx context.Context) {
	apps, err := m.store.ListAllApps()
	if err != nil {
		slog.Error("monitor: list apps for resources", "err", err)
		return
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	for _, app := range apps {
		memP90, cpuP90, samples, err := m.store.P90Metrics(app.ID, since)
		if err != nil {
			slog.Error("monitor: p90 metrics", "app", app.Slug, "err", err)
			continue
		}
		if samples < 10 {
			continue // not enough history yet
		}
		memLimitMB := clamp64(int64(memP90)*2/(1<<20), 64, 4096)
		cpuLimit := clampF(cpuP90*2/100, 0.5, 4.0)
		if memLimitMB != int64(app.MemLimitMB) || cpuLimit != app.CPULimit {
			if err := m.store.UpdateAppResources(app.ID, int(memLimitMB), cpuLimit); err != nil {
				slog.Error("monitor: update resources", "app", app.Slug, "err", err)
				continue
			}
			_ = m.store.RecordEvent(app.ID, store.EventResourceUpdate,
				fmt.Sprintf("detected limits → %d MB / %g CPU (from %d samples over 24h)", memLimitMB, cpuLimit, samples))
			slog.Info("monitor: detected resources",
				"app", app.Slug, "mem_mb", memLimitMB, "cpu", cpuLimit, "samples", samples)
		}
	}
}

func clamp64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
	if n, err := m.store.PruneBuildLogsBefore(now.Add(-buildLogsRetain)); err != nil {
		slog.Error("monitor: prune build logs", "err", err)
	} else if n > 0 {
		slog.Info("monitor: pruned build logs", "count", n)
	}
	if n, err := m.store.PruneAlertEventsBefore(now.Add(-uptimeRetain).Format(time.RFC3339Nano)); err != nil {
		slog.Error("monitor: prune alert events", "err", err)
	} else if n > 0 {
		slog.Info("monitor: pruned alert deliveries", "count", n)
	}
}
