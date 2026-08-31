package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/web/templates"
)

// statsWindow is the fleet stats lookback, matching the per-app History
// page (handlers_history.go).
const statsWindow = 30 * 24 * time.Hour

// handleStatsPage renders the fleet-wide build statistics: totals across
// all apps, a per-app table, and deploys per day.
func (s *Server) handleStatsPage(w http.ResponseWriter, r *http.Request) {
	since := time.Now().UTC().Add(-statsWindow)

	stats, err := s.store.DeploymentStatsAll(since)
	if err != nil {
		slog.Error("stats: deployment stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	perApp, err := s.store.DeploymentStatsPerApp(since)
	if err != nil {
		slog.Error("stats: per-app stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	perDay, err := s.store.DeploysPerDay(since)
	if err != nil {
		slog.Error("stats: per-day stats", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	render(w, r, http.StatusOK, templates.StatsPage(s.viewCtx(r), templates.FleetStats{
		Total:       stats.Total,
		SuccessRate: pct(stats.Succeeded, stats.Total),
		AvgBuildSec: stats.AvgBuildSec,
	}, perApp, perDay))
}
