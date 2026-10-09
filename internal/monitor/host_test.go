package monitor

import (
	"testing"

	"github.com/habibmuhammad/deploymate/internal/hostinfo"
)

func disk(l hostinfo.Level) []hostinfo.Finding {
	return []hostinfo.Finding{{Area: "disk", Level: l, Message: "Data is 92% full"}}
}

func TestHostWatchAlertsOnlyAfterASustainedProblem(t *testing.T) {
	w := newHostWatch()
	for i := 1; i < hostAlertAfter; i++ {
		if fire, _ := w.observe(disk(hostinfo.Warn)); len(fire) != 0 {
			t.Fatalf("reading %d alerted too early", i)
		}
	}
	fire, _ := w.observe(disk(hostinfo.Warn))
	if len(fire) != 1 {
		t.Fatalf("sustained problem should alert once, got %d", len(fire))
	}
	// Still bad: no repeat.
	for i := 0; i < 10; i++ {
		if fire, _ := w.observe(disk(hostinfo.Warn)); len(fire) != 0 {
			t.Fatal("the same problem alerted twice")
		}
	}
	// Getting worse is news.
	var worse []hostinfo.Finding
	for i := 0; i < hostAlertAfter; i++ {
		worse, _ = w.observe(disk(hostinfo.Crit))
	}
	if len(worse) != 1 || worse[0].Level != hostinfo.Crit {
		t.Errorf("escalation to critical should alert: %+v", worse)
	}
}

func TestHostWatchSpikesDoNotAlertAndRecoveryDoes(t *testing.T) {
	w := newHostWatch()
	// An on/off blip never reaches the streak.
	for i := 0; i < 10; i++ {
		lv := hostinfo.OK
		if i%2 == 0 {
			lv = hostinfo.Warn
		}
		var f []hostinfo.Finding
		if lv != hostinfo.OK {
			f = disk(lv)
		}
		if fire, _ := w.observe(f); len(fire) != 0 {
			t.Fatal("a flapping reading alerted")
		}
	}
	for i := 0; i < hostAlertAfter; i++ {
		w.observe(disk(hostinfo.Warn))
	}
	var recovered bool
	for i := 0; i < hostAlertAfter; i++ {
		_, recovered = w.observe(nil)
	}
	if !recovered {
		t.Error("expected a recovery after a clear stretch")
	}
	// Recovery is announced once.
	if _, again := w.observe(nil); again {
		t.Error("recovery announced twice")
	}
}
