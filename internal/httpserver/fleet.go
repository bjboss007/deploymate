package httpserver

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/fleet"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

const (
	heartbeatWindow  = 24 * time.Hour
	heartbeatBuckets = 24
)

// currentFleetState is the app's state right now, in the heartbeat's terms.
func currentFleetState(a store.App) string {
	switch {
	case a.Status == "failed", a.Status == "running" && a.Health == "unhealthy":
		return fleet.StateDown
	case a.Status == "running":
		return fleet.StateUp
	case a.Status == "stopped":
		return fleet.StateStopped
	}
	return ""
}

// attentionFor decides whether an app needs the owner and says why in a
// sentence: "bad" = it is not serving, "warn" = it is serving but something
// went wrong (a deploy failed and the previous version carries on).
func attentionFor(a store.App, last *store.Deployment) (level, reason string) {
	failedDeploy := last != nil && last.Status == "failed"
	why := func(prefix string) string {
		if failedDeploy && strings.TrimSpace(last.Error) != "" {
			return prefix + " " + clipText(last.Error, 110)
		}
		return prefix
	}
	switch {
	case a.Status == "failed":
		if failedDeploy {
			return "bad", why("The last deploy failed:")
		}
		return "bad", "Not running — its container is gone or stopped."
	case a.Status == "running" && a.Health == "unhealthy":
		return "bad", "Running, but failing its health check."
	case failedDeploy && a.Status == "running":
		return "warn", "The last deploy failed — the previous version is still serving."
	}
	return "", ""
}

func clipText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// appRows builds the fleet-board view of apps: newest meaningful deployment,
// the 24-hour heartbeat from recorded events and deployments, and whether
// the app needs attention. Read errors degrade that row (no strip), never
// the page.
func (s *Server) appRows(apps []store.App, now time.Time) []templates.AppRow {
	rows := make([]templates.AppRow, 0, len(apps))
	for _, a := range apps {
		row := templates.AppRow{App: a}
		deps, err := s.store.ListDeployments(a.ID, 40)
		if err != nil {
			slog.Error("fleet: list deployments", "app", a.Slug, "err", err)
		}
		for i := range deps {
			if deps[i].Kind != "scale" && deps[i].Kind != "resize" {
				d := deps[i]
				row.Last = &d
				break
			}
		}
		evs, err := s.store.ListEvents(a.ID, 300)
		if err != nil {
			slog.Error("fleet: list events", "app", a.Slug, "err", err)
		}
		cells := fleet.Heartbeat(now, heartbeatWindow, heartbeatBuckets, currentFleetState(a), evs, deps)
		row.UptimePct, row.UptimeKnown = fleet.Uptime(cells)
		for _, c := range cells {
			title := c.Start.Local().Format("Mon 15:04") + " · " + stateWord(c.State)
			switch c.Deploys {
			case 0:
			case 1:
				title += " · 1 deploy"
			default:
				title += fmt.Sprintf(" · %d deploys", c.Deploys)
			}
			row.Strip = append(row.Strip, templates.StripCell{State: c.State, Deploys: c.Deploys, Title: title})
		}
		row.Attention, row.Reason = attentionFor(a, row.Last)
		rows = append(rows, row)
	}
	return rows
}

func stateWord(state string) string {
	switch state {
	case fleet.StateUp:
		return "up"
	case fleet.StateDown:
		return "down"
	case fleet.StateStopped:
		return "stopped"
	}
	return "no data"
}

// summarize counts the board's one-line state.
func summarize(groups []templates.ProjectGroup) templates.FleetSummary {
	sm := templates.FleetSummary{Projects: len(groups)}
	for _, g := range groups {
		for _, r := range g.Rows {
			sm.Apps++
			switch {
			case r.Attention == "bad":
				sm.Failing++
			case r.App.Status == "running":
				sm.Healthy++
			default:
				sm.Stopped++
			}
		}
	}
	return sm
}
