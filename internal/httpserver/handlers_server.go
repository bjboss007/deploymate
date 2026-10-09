package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/habibmuhammad/deploymate/internal/hostinfo"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// HostSource reads the machine's vitals. The real one is hostinfo.Collector;
// the demo instance and tests supply fixed readings.
type HostSource interface {
	Snapshot(ctx context.Context) hostinfo.Snapshot
}

// SetHostSource replaces the host reader (demo instance, tests).
func (s *Server) SetHostSource(h HostSource) { s.host = h }

// SetDemoHost makes the Server page show a fictional machine and nothing from
// the real Docker engine (the website's demo instance).
func (s *Server) SetDemoHost(h HostSource) { s.host, s.demoHost = h, true }

// SetVersion tells the Server page which release is running.
func (s *Server) SetVersion(v string) { s.version = v }

// serverReport is one reading of the server, shared by the page and the API.
type serverReport struct {
	Snap     hostinfo.Snapshot
	Verdict  hostinfo.Verdict
	Platform hostinfo.Platform
	Docker   *templates.DiskStats
	TopApps  []templates.ServerApp
	DBBytes  int64
}

func (s *Server) serverReport(ctx context.Context) serverReport {
	rep := serverReport{}
	if s.host != nil {
		rep.Snap = s.host.Snapshot(ctx)
	}
	if rep.Snap.Time.IsZero() {
		rep.Snap.Time = time.Now()
	}

	if !s.demoHost {
		rep.Docker = s.diskStats(ctx)
	}
	if rep.Docker != nil {
		rep.Platform.DockerChecked = true
		rep.Platform.DockerUp = rep.Docker.Error == ""
	}
	// Traefik only runs as a container on a real server; a dev machine has none.
	if s.rt != nil && rep.Snap.Supported && rep.Platform.DockerUp {
		rep.Platform.TraefikChecked = true
		if info, err := s.rt.Inspect(ctx, "traefik"); err == nil && info.Running {
			rep.Platform.TraefikUp = true
		}
	}
	rep.Verdict = hostinfo.Evaluate(rep.Snap, rep.Platform)

	type use struct {
		name, slug string
		mem        uint64
		cpu        float64
	}
	var uses []use
	if apps, err := s.store.ListAllApps(); err == nil {
		for _, a := range apps {
			if a.Status != "running" {
				continue
			}
			if ms, err := s.store.ListMetrics(a.ID, 1); err == nil && len(ms) == 1 {
				uses = append(uses, use{a.Name, a.Slug, ms[0].MemBytes, ms[0].CPUPercent})
			}
		}
	}
	sort.Slice(uses, func(i, j int) bool { return uses[i].mem > uses[j].mem })
	for i, u := range uses {
		if i == 8 {
			break
		}
		rep.TopApps = append(rep.TopApps, templates.ServerApp{
			Name: u.name, Slug: u.slug, Mem: fmtMB(u.mem), CPU: fmt.Sprintf("%.1f%%", u.cpu),
		})
	}

	for _, ext := range []string{"", "-wal"} {
		if fi, err := os.Stat(filepath.Join(s.dataDir, "data.db"+ext)); err == nil {
			rep.DBBytes += fi.Size()
		}
	}
	return rep
}

func fmtMB(b uint64) string {
	if b < 1<<20 {
		return fmt.Sprintf("%d KB", b>>10)
	}
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	}
	return fmt.Sprintf("%d MB", b>>20)
}

func fmtRate(bps uint64) string {
	switch {
	case bps >= 1<<20:
		return fmt.Sprintf("%.1f MB/s", float64(bps)/(1<<20))
	case bps >= 1<<10:
		return fmt.Sprintf("%.0f KB/s", float64(bps)/(1<<10))
	}
	return fmt.Sprintf("%d B/s", bps)
}

func levelClass(l hostinfo.Level) string {
	switch l {
	case hostinfo.Warn:
		return "warn"
	case hostinfo.Crit:
		return "crit"
	}
	return "ok"
}

// usageLevel colours a "percent used" bar: warn from warnAt, crit from critAt.
func usageLevel(pct, warnAt, critAt float64) string {
	switch {
	case pct >= critAt:
		return "crit"
	case pct >= warnAt:
		return "warn"
	}
	return "ok"
}

func (s *Server) buildServerView(rep serverReport) templates.ServerView {
	sn := rep.Snap
	v := templates.ServerView{
		Supported: sn.Supported,
		Level:     levelClass(rep.Verdict.Level),
		Headline:  rep.Verdict.Headline,
		TopApps:   rep.TopApps,
		Docker:    rep.Docker,
		Sampled:   sn.Time.Format("15:04:05"),
	}
	for _, f := range rep.Verdict.Findings {
		v.Findings = append(v.Findings, templates.ServerFinding{Level: levelClass(f.Level), Message: f.Message})
	}

	if sn.Supported {
		cpu := templates.ServerGauge{Label: "CPU", Value: "n/a", Pct: -1, Level: "ok"}
		if sn.CPUKnown {
			cpu = templates.ServerGauge{Label: "CPU", Value: fmt.Sprintf("%.0f%%", sn.CPUPct), Pct: sn.CPUPct, Level: usageLevel(sn.CPUPct, 75, hostinfo.CPUWarnPct)}
		}
		cpu.Sub = fmt.Sprintf("load %.2f · %.2f · %.2f on %d cores", sn.Load1, sn.Load5, sn.Load15, sn.Cores)
		mem := templates.ServerGauge{
			Label: "Memory", Value: fmt.Sprintf("%.0f%%", sn.MemUsedPct()), Pct: sn.MemUsedPct(),
			Level: usageLevel(sn.MemUsedPct(), 100-hostinfo.MemWarnAvailPct, 100-hostinfo.MemCritAvailPct),
			Sub:   fmt.Sprintf("%s used of %s · %s cache", fmtMB(sn.MemUsed()), fmtMB(sn.MemTotal), fmtMB(sn.MemCached)),
		}
		v.Gauges = append(v.Gauges, cpu, mem)
		if sn.SwapTotal > 0 {
			sp := float64(sn.SwapUsed) / float64(sn.SwapTotal) * 100
			v.Gauges = append(v.Gauges, templates.ServerGauge{
				Label: "Swap", Value: fmtMB(sn.SwapUsed), Pct: sp, Level: usageLevel(sp, 50, 90),
				Sub: "of " + fmtMB(sn.SwapTotal),
			})
		} else {
			v.Gauges = append(v.Gauges, templates.ServerGauge{Label: "Swap", Value: "none", Pct: -1, Level: "ok", Sub: "no swap configured"})
		}
		if sn.NetKnown {
			v.Gauges = append(v.Gauges, templates.ServerGauge{
				Label: "Network in", Value: fmtRate(sn.NetRxBps), Pct: -1, Level: "ok", Sub: fmtRate(sn.NetTxBps) + " out",
			})
		}
		if sn.TempKnown {
			v.Gauges = append(v.Gauges, templates.ServerGauge{
				Label: "Temperature", Value: fmt.Sprintf("%.0f°C", sn.TempC), Pct: -1,
				Level: usageLevel(sn.TempC, hostinfo.TempWarnC, hostinfo.TempCritC),
			})
		}
		if sn.PressureKnown {
			v.Gauges = append(v.Gauges, templates.ServerGauge{
				Label: "Memory pressure", Value: fmt.Sprintf("%.0f%%", sn.MemPressure), Pct: -1,
				Level: usageLevel(sn.MemPressure, hostinfo.PressureWarn, 40), Sub: "time spent waiting on memory",
			})
		}
		for _, d := range sn.Disks {
			free := 100 - d.UsedPct()
			lvl := "ok"
			switch {
			case free < hostinfo.DiskCritFreePct:
				lvl = "crit"
			case free < hostinfo.DiskWarnFreePct:
				lvl = "warn"
			}
			v.Disks = append(v.Disks, templates.ServerDisk{
				Label: d.Label, Path: d.Path, Used: fmtMB(d.Used()), Total: fmtMB(d.Total), Free: fmtMB(d.Free),
				Pct: d.UsedPct(), Level: lvl,
			})
		}

		if sn.OS != "" {
			v.Machine = append(v.Machine, templates.ServerKV{Label: "System", Value: sn.OS})
		}
		if sn.Kernel != "" {
			v.Machine = append(v.Machine, templates.ServerKV{Label: "Kernel", Value: sn.Kernel})
		}
		v.Machine = append(v.Machine, templates.ServerKV{Label: "Up for", Value: hostinfo.FormatUptime(sn.Uptime)})
		if sn.Power.Present {
			kv := templates.ServerKV{Label: "Power", Value: fmt.Sprintf("plugged in, battery %d%%", sn.Power.Percent)}
			if sn.Power.OnBattery {
				kv = templates.ServerKV{Label: "Power", Value: fmt.Sprintf("on battery, %d%%", sn.Power.Percent), Level: "warn"}
			}
			v.Machine = append(v.Machine, kv)
		}
		if sn.RebootRequired {
			v.Machine = append(v.Machine, templates.ServerKV{Label: "Reboot", Value: "needed to finish an update", Level: "warn"})
		}
	}

	if rep.Platform.DockerChecked {
		kv := templates.ServerKV{Label: "Docker", Value: "running"}
		if !rep.Platform.DockerUp {
			kv = templates.ServerKV{Label: "Docker", Value: "not answering", Level: "crit"}
		}
		v.Platform = append(v.Platform, kv)
	}
	if rep.Platform.TraefikChecked {
		kv := templates.ServerKV{Label: "Traefik", Value: "running"}
		if !rep.Platform.TraefikUp {
			kv = templates.ServerKV{Label: "Traefik", Value: "not running", Level: "crit"}
		}
		v.Platform = append(v.Platform, kv)
	}
	ver := s.version
	if ver == "" {
		ver = "dev"
	}
	v.Platform = append(v.Platform,
		templates.ServerKV{Label: "DeployMate", Value: ver},
		templates.ServerKV{Label: "Database", Value: fmtMB(uint64(rep.DBBytes))},
	)
	return v
}

// handleServerPage renders /server. The page refreshes itself with HTMX, which
// asks for the same URL and gets only the body back.
func (s *Server) handleServerPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	v := s.buildServerView(s.serverReport(ctx))
	if r.Header.Get("HX-Request") == "true" {
		render(w, r, http.StatusOK, templates.ServerBody(v))
		return
	}
	render(w, r, http.StatusOK, templates.ServerPage(s.viewCtx(r), v))
}

// ---- API ------------------------------------------------------------------

type apiServerJSON struct {
	Status   string   `json:"status"` // ok | warning | critical
	Headline string   `json:"headline"`
	Problems []string `json:"problems,omitempty"`
	Linux    bool     `json:"host_metrics_available"`

	CPUPercent *float64  `json:"cpu_percent,omitempty"`
	Cores      int       `json:"cpu_cores,omitempty"`
	Load       []float64 `json:"load_average,omitempty"`

	MemoryTotalMB     uint64   `json:"memory_total_mb,omitempty"`
	MemoryAvailableMB uint64   `json:"memory_available_mb,omitempty"`
	MemoryUsedPercent *float64 `json:"memory_used_percent,omitempty"`
	SwapUsedMB        uint64   `json:"swap_used_mb,omitempty"`

	Disks []apiDiskJSON `json:"disks,omitempty"`

	TemperatureC *float64 `json:"temperature_c,omitempty"`
	OnBattery    *bool    `json:"on_battery,omitempty"`
	BatteryPct   *int     `json:"battery_percent,omitempty"`
	UptimeHours  float64  `json:"uptime_hours,omitempty"`
	RebootNeeded bool     `json:"reboot_needed,omitempty"`

	DockerUp            *bool  `json:"docker_running,omitempty"`
	TraefikUp           *bool  `json:"traefik_running,omitempty"`
	DockerImagesMB      uint64 `json:"docker_images_mb,omitempty"`
	DockerBuildCacheMB  uint64 `json:"docker_build_cache_mb,omitempty"`
	DockerReclaimableMB uint64 `json:"docker_reclaimable_mb,omitempty"`
	Version             string `json:"version"`

	BusiestApps []apiBusyAppJSON `json:"busiest_apps,omitempty"`
}

type apiDiskJSON struct {
	Label       string  `json:"label"`
	UsedPercent float64 `json:"used_percent"`
	FreeMB      uint64  `json:"free_mb"`
	TotalMB     uint64  `json:"total_mb"`
}

type apiBusyAppJSON struct {
	Slug   string `json:"slug"`
	Memory string `json:"memory"`
	CPU    string `json:"cpu"`
}

// handleAPIServer: GET /api/v1/server — read scope. Numbers only; no paths,
// hostnames, addresses or anything an attacker could use to map the box.
func (s *Server) handleAPIServer(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	rep := s.serverReport(ctx)
	sn := rep.Snap
	out := apiServerJSON{
		Status:   map[hostinfo.Level]string{hostinfo.OK: "ok", hostinfo.Warn: "warning", hostinfo.Crit: "critical"}[rep.Verdict.Level],
		Headline: rep.Verdict.Headline, Linux: sn.Supported, Version: s.version,
	}
	for _, f := range rep.Verdict.Findings {
		out.Problems = append(out.Problems, f.Message)
	}
	if sn.Supported {
		out.Cores = sn.Cores
		if sn.CPUKnown {
			c := round1(sn.CPUPct)
			out.CPUPercent = &c
		}
		out.Load = []float64{sn.Load1, sn.Load5, sn.Load15}
		out.MemoryTotalMB, out.MemoryAvailableMB = sn.MemTotal>>20, sn.MemAvailable>>20
		m := round1(sn.MemUsedPct())
		out.MemoryUsedPercent = &m
		out.SwapUsedMB = sn.SwapUsed >> 20
		for _, d := range sn.Disks {
			out.Disks = append(out.Disks, apiDiskJSON{Label: d.Label, UsedPercent: round1(d.UsedPct()), FreeMB: d.Free >> 20, TotalMB: d.Total >> 20})
		}
		if sn.TempKnown {
			t := round1(sn.TempC)
			out.TemperatureC = &t
		}
		if sn.Power.Present {
			b, p := sn.Power.OnBattery, sn.Power.Percent
			out.OnBattery, out.BatteryPct = &b, &p
		}
		out.UptimeHours = round1(sn.Uptime.Hours())
		out.RebootNeeded = sn.RebootRequired
	}
	if rep.Platform.DockerChecked {
		up := rep.Platform.DockerUp
		out.DockerUp = &up
	}
	if rep.Platform.TraefikChecked {
		up := rep.Platform.TraefikUp
		out.TraefikUp = &up
	}
	if d := rep.Docker; d != nil && d.Error == "" {
		out.DockerImagesMB, out.DockerBuildCacheMB, out.DockerReclaimableMB = d.ImagesBytes>>20, d.BuildCacheBytes>>20, d.ReclaimableBytes>>20
	}
	for _, a := range rep.TopApps {
		out.BusiestApps = append(out.BusiestApps, apiBusyAppJSON{Slug: a.Slug, Memory: a.Mem, CPU: a.CPU})
	}
	slog.Debug("api: server", "status", out.Status)
	apiJSON(w, http.StatusOK, out)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
