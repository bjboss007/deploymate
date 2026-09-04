package backup

import (
	"context"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestSchedulerDue(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	enable(t, mgr, svc, func(c *store.BackupConfig) { c.Schedule = "* * * * *" })
	rt.execFn = dumpScript(rt, []byte("dump"))

	// Last run 2 minutes ago → the per-minute schedule's next firing is in
	// the past → due.
	if err := st.SetBackupLastRun(svc.ID, time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(st, mgr)
	s.tick(context.Background())
	if !hasEvent(st, store.EventBackupOK) {
		t.Fatal("due window did not run")
	}
}

func TestSchedulerNotDue(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	enable(t, mgr, svc, func(c *store.BackupConfig) { c.Schedule = "* * * * *" })
	rt.execFn = dumpScript(rt, []byte("dump"))

	// Last run just now → next firing is a minute out → not due.
	if err := st.SetBackupLastRun(svc.ID, store.Now()); err != nil {
		t.Fatal(err)
	}
	s := NewScheduler(st, mgr)
	s.tick(context.Background())
	if len(rt.execCalls()) != 0 {
		t.Fatalf("not-due window ran anyway: %+v", rt.execCalls())
	}
}

func TestSchedulerFirstRunImmediate(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	enable(t, mgr, svc) // fresh row: last_run_at = ""
	rt.execFn = dumpScript(rt, []byte("dump"))

	s := NewScheduler(st, mgr)
	s.tick(context.Background())
	if !hasEvent(st, store.EventBackupOK) {
		t.Fatal("fresh opt-in should run on the first tick (no last_run_at yet)")
	}
}

func TestSchedulerSkipsInflight(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	enable(t, mgr, svc)
	rt.execFn = dumpScript(rt, []byte("dump"))

	if !mgr.acquire(svc.ID) {
		t.Fatal("acquire failed")
	}
	defer mgr.release(svc.ID)

	s := NewScheduler(st, mgr)
	s.tick(context.Background())
	if len(rt.execCalls()) != 0 {
		t.Fatalf("in-flight service ran anyway: %+v", rt.execCalls())
	}
}

func TestSchedulerBadScheduleRowTolerated(t *testing.T) {
	mgr, st, svc, rt, _ := newTestManager(t)
	enable(t, mgr, svc)
	// Corrupt the schedule behind SaveConfig's back (it validates).
	cfg, err := st.GetBackupConfig(svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Schedule = "utter garbage"
	if err := st.SaveBackupConfig(cfg); err != nil {
		t.Fatal(err)
	}

	s := NewScheduler(st, mgr)
	s.tick(context.Background()) // must not panic
	if len(rt.execCalls()) != 0 {
		t.Fatal("bad schedule row must be skipped, not run")
	}
}
