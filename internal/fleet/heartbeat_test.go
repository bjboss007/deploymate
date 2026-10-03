package fleet

import (
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

var now = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

func ev(ago time.Duration, kind string) store.Event {
	return store.Event{TS: now.Add(-ago).Format(time.RFC3339Nano), Kind: kind}
}

func dep(ago time.Duration, status, kind string) store.Deployment {
	return store.Deployment{CreatedAt: now.Add(-ago).Format(time.RFC3339Nano), Status: status, Kind: kind}
}

func states(cs []Cell) string {
	out := ""
	for _, c := range cs {
		switch c.State {
		case StateUp:
			out += "U"
		case StateDown:
			out += "D"
		case StateStopped:
			out += "S"
		default:
			out += "."
		}
	}
	return out
}

// Started before the window, down for ~1h20m starting 5.5 h ago, stopped for
// the last hour: 24 hourly buckets, oldest first.
func TestHeartbeatTimeline(t *testing.T) {
	events := []store.Event{
		ev(30*time.Hour, store.EventAppStarted),
		ev(5*time.Hour+30*time.Minute, store.EventHealthUnhealthy),
		ev(4*time.Hour+10*time.Minute, store.EventHealthRecovered),
		ev(1*time.Hour, store.EventAppStopped),
	}
	deploys := []store.Deployment{dep(3*time.Hour+20*time.Minute, "running", "deploy"), dep(9*time.Hour, "failed", "deploy"), dep(2*time.Hour, "running", "scale")}
	cells := Heartbeat(now, 24*time.Hour, 24, StateStopped, events, deploys)
	// buckets 0..17 up; 18 and 19 (the two hours holding 5:30..4:10 ago) down;
	// 20..22 up; last hour stopped.
	if got, want := states(cells), "UUUUUUUUUUUUUUUUUUDDUUUS"; len(got) != 24 || got != want {
		// bucket index 18 = [-6h,-5h), 19 = [-5h,-4h): the outage straddles them.
		t.Errorf("states = %s, want %s", got, want)
	}
	// The running deploy 3h20m ago is in bucket 20 ([-4h,-3h) holds -3h20m → index 20); the failed one
	// still counts as a deploy (it happened), the scale does not.
	total := 0
	for _, c := range cells {
		total += c.Deploys
	}
	if total != 2 {
		t.Errorf("deploys counted = %d, want 2 (scale excluded)", total)
	}
	if cells[20].Deploys != 1 {
		t.Errorf("deploy at -3h20m landed in the wrong bucket: %+v", cells[20])
	}
}

// The strip can't disagree with the badge: an app that is down right now
// ends in down even when no event says so.
func TestHeartbeatAnchorsToCurrentState(t *testing.T) {
	cells := Heartbeat(now, 24*time.Hour, 24, StateDown, []store.Event{ev(30*time.Hour, store.EventAppStarted)}, nil)
	if got := states(cells); got[0] != 'U' || got[len(got)-1] != 'D' {
		t.Errorf("states = %s, want up at the start and down at the end", got)
	}
}

func TestHeartbeatNoHistory(t *testing.T) {
	cells := Heartbeat(now, 24*time.Hour, 24, "", nil, nil)
	if got := states(cells); got != "........................" {
		t.Errorf("states = %s, want all unknown", got)
	}
	if _, known := Uptime(cells); known {
		t.Error("uptime of an unknown history must be unknown")
	}
	if Heartbeat(now, 0, 24, "", nil, nil) != nil || Heartbeat(now, time.Hour, 0, "", nil, nil) != nil {
		t.Error("degenerate inputs must return nil")
	}
}

func TestUptime(t *testing.T) {
	cs := []Cell{{State: StateUp}, {State: StateUp}, {State: StateUp}, {State: StateDown}, {State: StateStopped}, {State: StateNone}}
	if pct, ok := Uptime(cs); !ok || pct != 75 {
		t.Errorf("Uptime = %v, %v; want 75, true (stopped/unknown excluded)", pct, ok)
	}
}
