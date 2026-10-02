package templates

import (
	"testing"
	"time"
)

func TestAgoAt(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339Nano) }
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{10 * time.Second, "just now"},
		{50 * time.Second, "1 min ago"},
		{5 * time.Minute, "5 min ago"},
		{3 * time.Hour, "3 h ago"},
		{50 * time.Hour, "2 d ago"},
	} {
		if got := agoAt(ts(tc.d), now); got != tc.want {
			t.Errorf("agoAt(-%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if got := agoAt("not a time", now); got != "—" {
		t.Errorf("bad input = %q", got)
	}
}

func TestClip(t *testing.T) {
	if got := clip("  hello  ", 10); got != "hello" {
		t.Errorf("trim: %q", got)
	}
	if got := clip("héllo wörld", 5); got != "héllo…" {
		t.Errorf("rune-safe clip: %q", got)
	}
}
