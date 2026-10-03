// Package fleet derives the at-a-glance picture of an app for the fleet
// board: a "heartbeat" strip of the last day built from what DeployMate
// already records (health and lifecycle events, deployments) — no new
// sampling is needed.
package fleet

import (
	"sort"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// Bucket states, worst first when a bucket covers more than one.
const (
	StateDown    = "down"    // failing its health check, or failed
	StateUp      = "up"      // running and answering
	StateStopped = "stopped" // deliberately stopped
	StateNone    = "none"    // nothing known (before the app first ran)
)

// Cell is one slice of the heartbeat strip.
type Cell struct {
	Start   time.Time
	State   string
	Deploys int // deployments that landed in this slice
}

type transition struct {
	at    time.Time
	state string
}

// stateOf maps a lifecycle event to the state it puts the app in ("" =
// the event says nothing about availability).
func stateOf(kind string) string {
	switch kind {
	case store.EventAppStarted, store.EventHealthRecovered, store.EventAppHealed, store.EventAppRestarted:
		return StateUp
	case store.EventHealthUnhealthy:
		return StateDown
	case store.EventAppStopped:
		return StateStopped
	}
	return ""
}

// Heartbeat splits the window ending at now into `buckets` equal slices and
// gives each the worst state the app was in during it (down > up > stopped >
// none), plus how many deployments landed in it. current is the app's state
// right now (StateUp/Down/Stopped, "" = unknown): it anchors the tail so the
// strip can never disagree with the status badge next to it. Successful
// deployments count as "up" from when they ran; failed ones change nothing
// (the previous version keeps serving).
func Heartbeat(now time.Time, window time.Duration, buckets int, current string, events []store.Event, deploys []store.Deployment) []Cell {
	if buckets <= 0 || window <= 0 {
		return nil
	}
	var ts []transition
	for _, e := range events {
		if st := stateOf(e.Kind); st != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
				ts = append(ts, transition{t, st})
			}
		}
	}
	for _, d := range deploys {
		if d.Status != "running" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err == nil {
			ts = append(ts, transition{t, StateUp})
		}
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].at.Before(ts[j].at) })
	// Anchor the tail on `current`. When no recorded event explains a change
	// we only know it holds NOW, so claim just the last slice rather than
	// painting the whole window with a state nothing recorded.
	if current != "" && (len(ts) == 0 || ts[len(ts)-1].state != current) {
		at := now.Add(-window / time.Duration(buckets))
		if len(ts) > 0 && ts[len(ts)-1].at.After(at) {
			at = ts[len(ts)-1].at
		}
		ts = append(ts, transition{at, current})
	}

	start := now.Add(-window)
	step := window / time.Duration(buckets)
	cells := make([]Cell, buckets)
	for i := range cells {
		cells[i] = Cell{Start: start.Add(time.Duration(i) * step), State: StateNone}
	}
	rank := map[string]int{StateNone: 0, StateStopped: 1, StateUp: 2, StateDown: 3}
	// Walk the buckets keeping the state in effect; a transition exactly at a
	// bucket's start belongs to that bucket, one inside it can only make the
	// bucket worse (down > up > stopped > none).
	cur := StateNone
	idx := 0
	for i := range cells {
		bs, be := cells[i].Start, cells[i].Start.Add(step)
		for idx < len(ts) && !ts[idx].at.After(bs) {
			cur = ts[idx].state
			idx++
		}
		worst := cur
		for idx < len(ts) && ts[idx].at.Before(be) {
			cur = ts[idx].state
			if rank[cur] > rank[worst] {
				worst = cur
			}
			idx++
		}
		cells[i].State = worst
	}
	for _, d := range deploys {
		if d.Kind == "scale" || d.Kind == "resize" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, d.CreatedAt)
		if err != nil || t.Before(start) || t.After(now) {
			continue
		}
		i := int(t.Sub(start) / step)
		if i >= buckets {
			i = buckets - 1
		}
		cells[i].Deploys++
	}
	return cells
}

// Uptime is the share of known (up or down) slices that were up; known is
// false when there is nothing to base it on.
func Uptime(cells []Cell) (pct float64, known bool) {
	up, down := 0, 0
	for _, c := range cells {
		switch c.State {
		case StateUp:
			up++
		case StateDown:
			down++
		}
	}
	if up+down == 0 {
		return 0, false
	}
	return float64(up) / float64(up+down) * 100, true
}
