package httpserver

import (
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// handleAppHistory renders the learnable history: a merged timeline of
// deployments + events, and derived stats (success rate, build time,
// uptime, MTTR, incidents).
func (s *Server) handleAppHistory(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		slog.Error("history: get project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	since := time.Now().UTC().Add(-statsWindow)

	stats, err := s.store.DeploymentStatsFor(app.ID, since)
	if err != nil {
		slog.Error("history: deployment stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	uptimePct, uptimeSamples := 0.0, 0
	if domains, err := s.store.ListDomains(app.ID); err == nil {
		for _, dm := range domains {
			pct, n, err := s.store.UptimePercent(dm.ID, since)
			if err == nil && n > 0 {
				uptimePct = pct
				uptimeSamples = n
				break // one domain is enough for the stat strip
			}
		}
	}

	mttr, incidents, err := s.store.MTTRSeconds(app.ID)
	if err != nil {
		slog.Error("history: mttr", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Merge deployments + events into one chronological feed.
	items := []templates.HistoryItem{}
	deployments, err := s.store.ListDeployments(app.ID, 100)
	if err != nil {
		slog.Error("history: list deployments", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, d := range deployments {
		item := templates.HistoryItem{TS: d.CreatedAt, Kind: d.Kind, Label: "deploy " + shortDeployIDLocal(d.ID)}
		switch d.Status {
		case "running":
			item.Tone = "good"
			if d.CommitMessage != "" {
				item.Data = d.CommitMessage
			} else {
				item.Data = "deployed"
			}
		case "failed":
			item.Tone, item.Data = "bad", d.Error
		default:
			item.Tone, item.Data = "neutral", d.Status
		}
		items = append(items, item)
	}
	events, err := s.store.ListEvents(app.ID, 300)
	if err != nil {
		slog.Error("history: list events", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for _, e := range events {
		item := templates.HistoryItem{TS: e.TS, Kind: e.Kind, Label: e.Kind, Data: e.Data}
		switch e.Kind {
		case store.EventHealthUnhealthy:
			item.Tone = "bad"
		case store.EventHealthRecovered:
			item.Tone = "good"
		case store.EventResourceResized:
			item.Tone = "good"
		default:
			item.Tone = "neutral"
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TS > items[j].TS })
	if len(items) > 200 {
		items = items[:200]
	}

	viewStats := templates.HistoryStats{
		Deploys:      stats.Total,
		SuccessRate:  pct(stats.Succeeded, stats.Total),
		AvgBuildSec:  stats.AvgBuildSec,
		UptimePct:    uptimePct,
		UptimeSamples: uptimeSamples,
		MTTRMin:      mttr / 60,
		Incidents:    incidents,
	}
	render(w, r, http.StatusOK, templates.HistoryPage(s.viewCtx(r), project, app, viewStats, items))
}

func pct(part, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(part) / float64(total) * 100
}

func shortDeployIDLocal(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
