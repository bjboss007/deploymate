package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// handleAppMetricsJSON serves chart data for the app's metrics panel:
// the newest ~60 sampling ticks (5 minutes at the 5s cadence), totalled
// across replicas — or one replica's samples with ?replica=r2.
func (s *Server) handleAppMetricsJSON(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	var metrics []store.Metric
	var err error
	if rep := r.URL.Query().Get("replica"); rep != "" {
		slot, convErr := strconv.Atoi(strings.TrimPrefix(rep, "r"))
		if convErr != nil || !strings.HasPrefix(rep, "r") || slot < 1 || slot > store.MaxReplicas {
			http.Error(w, "replica must be r1..r5", http.StatusBadRequest)
			return
		}
		metrics, err = s.store.ListSlotMetrics(app.ID, slot, 60)
	} else {
		metrics, err = s.store.ListMetrics(app.ID, 60)
	}
	if err != nil {
		slog.Error("metrics: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type point struct {
		T string  `json:"t"`
		V float64 `json:"v"`
	}
	type series struct {
		CPU []point `json:"cpu"`
		Mem []point `json:"mem"`
	}
	out := series{CPU: []point{}, Mem: []point{}}
	for _, m := range metrics {
		out.CPU = append(out.CPU, point{T: m.TS, V: round2(m.CPUPercent)})
		out.Mem = append(out.Mem, point{T: m.TS, V: round2(float64(m.MemBytes) / (1024 * 1024))})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleAppUptimeJSON serves each domain's recent checks for the uptime
// strip. The app page renders dots server-side; this stays for future use.
func (s *Server) handleAppUptimeJSON(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	domains, err := s.store.ListDomains(app.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type domainUptime struct {
		Hostname string  `json:"hostname"`
		OK       bool    `json:"ok"`
		Uptime   float64 `json:"uptime_pct"` // last 24h from recent checks
	}
	out := []domainUptime{}
	for _, d := range domains {
		checks, err := s.store.ListUptimeChecks(d.ID, 200)
		if err != nil {
			continue
		}
		if len(checks) == 0 {
			out = append(out, domainUptime{Hostname: d.Hostname, OK: false, Uptime: 0})
			continue
		}
		okCount := 0
		lastOK := false
		for _, c := range checks {
			lastOK = c.OK
			if c.OK {
				okCount++
			}
		}
		out = append(out, domainUptime{
			Hostname: d.Hostname,
			OK:       lastOK,
			Uptime:   round2(float64(okCount) / float64(len(checks)) * 100),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}
