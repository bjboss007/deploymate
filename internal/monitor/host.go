package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/hostinfo"
	"github.com/habibmuhammad/deploymate/internal/store"
)

const (
	hostSampleEvery = time.Minute
	hostRetain      = 7 * 24 * time.Hour
	// A problem must last this many consecutive readings (minutes) before an
	// alert goes out, so a CPU spike or a brief memory dip never pages anyone.
	hostAlertAfter = 3
)

// HostSource reads the machine's vitals (hostinfo.Collector).
type HostSource interface {
	Snapshot(ctx context.Context) hostinfo.Snapshot
}

// SetHost turns on host sampling and alerts; called once at startup before Run.
func (m *Monitor) SetHost(h HostSource) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.host = h
}

// hostWatch decides when a problem is worth an alert: it must persist for
// hostAlertAfter readings, and each (area, level) alerts once until the whole
// machine has been clear for as long.
type hostWatch struct {
	streak    map[string]int // area+level -> consecutive readings
	alerted   map[string]bool
	clearRuns int
}

func newHostWatch() *hostWatch {
	return &hostWatch{streak: map[string]int{}, alerted: map[string]bool{}}
}

// observe takes one reading's findings and returns the ones to alert about
// now, and whether the machine has just recovered.
func (w *hostWatch) observe(findings []hostinfo.Finding) (fire []hostinfo.Finding, recovered bool) {
	seen := map[string]bool{}
	for _, f := range findings {
		if f.Level == hostinfo.OK {
			continue
		}
		key := f.Area + "/" + f.Level.String()
		seen[key] = true
		w.streak[key]++
		if w.streak[key] >= hostAlertAfter && !w.alerted[key] {
			w.alerted[key] = true
			fire = append(fire, f)
		}
	}
	for k := range w.streak {
		if !seen[k] {
			delete(w.streak, k)
		}
	}
	if len(seen) == 0 {
		w.clearRuns++
		if w.clearRuns >= hostAlertAfter && len(w.alerted) > 0 {
			w.alerted = map[string]bool{}
			return fire, true
		}
	} else {
		w.clearRuns = 0
	}
	return fire, false
}

// sampleHost stores one reading and alerts on sustained problems.
func (m *Monitor) sampleHost(ctx context.Context) {
	m.mu.Lock()
	h := m.host
	m.mu.Unlock()
	if h == nil {
		return
	}
	sn := h.Snapshot(ctx)
	if !sn.Supported {
		return
	}
	hm := store.HostMetric{
		TS: store.Now(), CPU: sn.CPUPct, Mem: sn.MemUsedPct(), Load1: sn.Load1,
		NetRx: sn.NetRxBps, NetTx: sn.NetTxBps, TempC: -1,
	}
	if len(sn.Disks) > 0 {
		hm.Disk = sn.Disks[0].UsedPct()
	}
	if sn.TempKnown {
		hm.TempC = sn.TempC
	}
	if err := m.store.InsertHostMetric(hm); err != nil {
		slog.Error("monitor: insert host metric", "err", err)
	}

	plat := hostinfo.Platform{TraefikChecked: true}
	if info, err := m.rt.Inspect(ctx, "traefik"); err == nil && info.Running {
		plat.TraefikUp = true
	}
	v := hostinfo.Evaluate(sn, plat)

	m.mu.Lock()
	if m.hostWatch == nil {
		m.hostWatch = newHostWatch()
	}
	fire, recovered := m.hostWatch.observe(v.Findings)
	m.mu.Unlock()

	if len(fire) > 0 {
		var lines []string
		worst := hostinfo.Warn
		for _, f := range fire {
			lines = append(lines, f.Message)
			if f.Level > worst {
				worst = f.Level
			}
		}
		m.alerts.Notify(alerts.EventHostProblem,
			fmt.Sprintf("server %s: %s", worst, fire[0].Message), strings.Join(lines, "\n"))
		slog.Warn("monitor: host problem", "findings", len(fire))
	}
	if recovered {
		m.alerts.Notify(alerts.EventHostRecovered, "server recovered", "CPU, memory, disk and the platform are back to normal.")
	}
}
