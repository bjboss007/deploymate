package httpserver

import (
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

func TestDescribeEvent(t *testing.T) {
	for kind, want := range map[string][3]string{
		store.EventHealthUnhealthy: {"Became unhealthy", "health", "bad"},
		store.EventHealthRecovered: {"Recovered", "health", "good"},
		store.EventEnvChanged:      {"Environment variables changed", "config", "neutral"},
		store.EventAppStopped:      {"Stopped", "lifecycle", "neutral"},
		"some_future_event":        {"Some future event", "config", "neutral"},
	} {
		title, cat, tone := describeEvent(kind)
		if title != want[0] || cat != want[1] || tone != want[2] {
			t.Errorf("describeEvent(%q) = %q/%q/%q, want %v", kind, title, cat, tone, want)
		}
	}
}

func TestDescribeDeployment(t *testing.T) {
	for _, tc := range []struct {
		d     store.Deployment
		title string
		tone  string
	}{
		{store.Deployment{Status: "running", Kind: "deploy"}, "Deployed", "good"},
		{store.Deployment{Status: "running", Kind: "rollback"}, "Rolled back", "good"},
		{store.Deployment{Status: "failed"}, "Deploy failed", "bad"},
		{store.Deployment{Status: "building"}, "Deploy in progress", "neutral"},
	} {
		if title, tone := describeDeployment(tc.d); title != tc.title || tone != tc.tone {
			t.Errorf("%+v -> %q/%q, want %q/%q", tc.d, title, tone, tc.title, tc.tone)
		}
	}
}

func TestGroupHistoryByDay(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	ts := func(d time.Duration) string { return now.Add(-d).UTC().Format(time.RFC3339Nano) }
	items := []templates.HistoryItem{
		{TS: ts(time.Hour)}, {TS: ts(2 * time.Hour)}, // today
		{TS: ts(20 * time.Hour)},     // yesterday
		{TS: ts(5 * 24 * time.Hour)}, // older
		{TS: "garbage"},
	}
	days := groupHistoryByDay(items, now)
	if len(days) != 4 || days[0].Label != "Today" || len(days[0].Items) != 2 || days[1].Label != "Yesterday" || days[3].Label != "Earlier" {
		t.Fatalf("days = %+v", days)
	}
	if days[0].Items[0].Clock != now.Add(-time.Hour).Local().Format("15:04") {
		t.Errorf("clock = %q", days[0].Items[0].Clock)
	}
}
