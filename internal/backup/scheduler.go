package backup

import (
	"context"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// Scheduler fires each enabled service's backup config on its cron window.
// The driver is ours (not robfig's running goroutines): robfig only parses
// schedules and computes next() — last_run_at is persisted, so a server
// restart can never double-fire a window and a service that was down during
// its window simply waits for the next one (missed windows are never
// back-filled; "Back up now" covers the urgent gap).
type Scheduler struct {
	st  *store.Store
	mgr *Manager
	now func() time.Time
}

// NewScheduler builds a Scheduler ticking once a minute.
func NewScheduler(st *store.Store, mgr *Manager) *Scheduler {
	return &Scheduler{st: st, mgr: mgr, now: time.Now}
}

// Run ticks until ctx is done: an immediate first pass, then every minute —
// the same shape as the monitor's tick loop.
func (s *Scheduler) Run(ctx context.Context) {
	s.tick(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick evaluates every enabled config. Per-service single-flight happens in
// Manager.run (which acquires and, on success or failure, records
// last_run_at), so a long dump neither overlaps itself nor retries.
func (s *Scheduler) tick(ctx context.Context) {
	targets, err := s.st.ListEnabledBackupTargets()
	if err != nil {
		slog.Error("backup scheduler: list targets", "err", err)
		return
	}
	for _, t := range targets {
		if s.mgr.inFlight(t.ServiceID) {
			continue // another backup/restore for this service is running
		}
		sched, err := cron.ParseStandard(t.Schedule)
		if err != nil {
			slog.Warn("backup scheduler: bad schedule", "slug", t.Service.Slug, "schedule", t.Schedule, "err", err)
			continue
		}
		last := s.parseLastRun(t.LastRunAt)
		// Cron fields are server-LOCAL: last_run_at is stored UTC, so move
		// it into the local zone before asking for the next firing.
		next := sched.Next(last.Local())
		if next.After(s.now()) {
			continue // not due yet
		}
		s.mgr.run(ctx, t.Service, t.BackupConfig)
	}
}

// parseLastRun turns a stored last_run_at back into a time. Empty (never
// run) parses as the zero time — the first firing after year zero is always
// in the past, so a fresh opt-in runs on the first tick after enabling
// rather than waiting up to a day. An unreadable value is treated the same
// way (one odd-time run repairs the field).
func (s *Scheduler) parseLastRun(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		slog.Warn("backup scheduler: bad last_run_at", "value", v, "err", err)
		return time.Time{}
	}
	return t
}
