package httpserver

import (
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// describeEvent turns a recorded event kind into the sentence a person would
// say, its category (for the page's filters) and its tone. Unknown kinds fall
// back to the kind with underscores read as spaces, so a new event never
// renders as nothing.
func describeEvent(kind string) (title, category, tone string) {
	switch kind {
	case store.EventAppStarted:
		return "Started", "lifecycle", "neutral"
	case store.EventAppStopped:
		return "Stopped", "lifecycle", "neutral"
	case store.EventAppRestarted:
		return "Restarted", "lifecycle", "neutral"
	case store.EventAppDeleted:
		return "Deleted", "lifecycle", "bad"
	case store.EventHealthUnhealthy:
		return "Became unhealthy", "health", "bad"
	case store.EventHealthRecovered:
		return "Recovered", "health", "good"
	case store.EventAppHealed:
		return "Healed automatically", "health", "good"
	case store.EventResourceResized:
		return "Resources resized", "config", "good"
	case store.EventResourceUpdate:
		return "Resource limits updated", "config", "neutral"
	case store.EventAppScaled:
		return "Replica count changed", "config", "neutral"
	case store.EventHealthPathChanged:
		return "Health path changed", "config", "neutral"
	case store.EventEnvChanged:
		return "Environment variables changed", "config", "neutral"
	case store.EventEnvRemoved:
		return "Environment variable removed", "config", "neutral"
	case store.EventRuntimeChanged:
		return "Build method changed", "config", "neutral"
	case store.EventGitConnected:
		return "Git repository connected", "config", "neutral"
	case store.EventEnvironmentChanged:
		return "Environment changed", "config", "neutral"
	case store.EventDNSRecordFailed:
		return "Preview address could not be created", "health", "bad"
	}
	t := strings.ReplaceAll(kind, "_", " ")
	if t != "" {
		t = strings.ToUpper(t[:1]) + t[1:]
	}
	return t, "config", "neutral"
}

// describeDeployment titles a deployment row by what happened to it.
func describeDeployment(d store.Deployment) (title, tone string) {
	switch d.Status {
	case "running":
		switch d.Kind {
		case "rollback":
			return "Rolled back", "good"
		case "redeploy":
			return "Redeployed with current settings", "good"
		}
		return "Deployed", "good"
	case "failed":
		return "Deploy failed", "bad"
	case "queued":
		return "Deploy queued", "neutral"
	case "building":
		return "Deploy in progress", "neutral"
	}
	return "Deploy " + d.Status, "neutral"
}

// groupHistoryByDay splits a newest-first feed into local calendar days
// ("Today", "Yesterday", "Mon 29 Sep"), keeping order.
func groupHistoryByDay(items []templates.HistoryItem, now time.Time) []templates.HistoryDay {
	var out []templates.HistoryDay
	today := now.Local().Format("2006-01-02")
	yesterday := now.Local().AddDate(0, 0, -1).Format("2006-01-02")
	for _, it := range items {
		t, err := time.Parse(time.RFC3339Nano, it.TS)
		label := "Earlier"
		if err == nil {
			day := t.Local().Format("2006-01-02")
			switch day {
			case today:
				label = "Today"
			case yesterday:
				label = "Yesterday"
			default:
				label = t.Local().Format("Mon 2 Jan")
			}
			it.Clock = t.Local().Format("15:04")
		}
		if n := len(out); n == 0 || out[n-1].Label != label {
			out = append(out, templates.HistoryDay{Label: label})
		}
		out[len(out)-1].Items = append(out[len(out)-1].Items, it)
	}
	return out
}
