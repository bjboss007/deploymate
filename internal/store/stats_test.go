package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

// newStatsStore opens a fresh store with a project and two apps.
func newStatsStore(t *testing.T) (*Store, App, App) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(Project{UserID: owner.ID, Name: "T", Slug: "t"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	a1, err := st.CreateApp(App{ProjectID: proj.ID, Name: "Alpha", Slug: "alpha"})
	if err != nil {
		t.Fatalf("create app alpha: %v", err)
	}
	a2, err := st.CreateApp(App{ProjectID: proj.ID, Name: "Beta", Slug: "beta"})
	if err != nil {
		t.Fatalf("create app beta: %v", err)
	}
	return st, a1, a2
}

// seedDeployment inserts a deployment at a controlled creation time with a
// build duration of buildSec (empty started/finished when buildSec <= 0).
func seedDeployment(t *testing.T, st *Store, appID, kind, status string, created time.Time, buildSec int) {
	t.Helper()
	d := Deployment{AppID: appID, Kind: kind, Status: status}
	if buildSec > 0 {
		d.StartedAt = created.Add(time.Second).UTC().Format(time.RFC3339Nano)
		d.FinishedAt = created.Add(time.Duration(buildSec+1) * time.Second).UTC().Format(time.RFC3339Nano)
	}
	d, err := st.CreateDeployment(d)
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if _, err := st.db.Exec(`UPDATE deployments SET created_at = ? WHERE id = ?`, created.UTC().Format(time.RFC3339Nano), d.ID); err != nil {
		t.Fatalf("backdate deployment: %v", err)
	}
}

// TestDeploymentStatsAll computes fleet totals: per-app rows roll up,
// failed and outside-window rows are handled correctly, and avg build time
// only counts deployments with both timestamps.
func TestDeploymentStatsAll(t *testing.T) {
	st, a1, a2 := newStatsStore(t)
	now := time.Now().UTC()

	seedDeployment(t, st, a1.ID, "deploy", "running", now, 60)
	// Manual (image) deploys count toward fleet stats too.
	seedDeployment(t, st, a1.ID, "manual", "running", now, 30)
	seedDeployment(t, st, a1.ID, "deploy", "failed", now, 0)
	seedDeployment(t, st, a2.ID, "deploy", "running", now, 15)
	// Outside the window: must not count.
	seedDeployment(t, st, a1.ID, "deploy", "running", now.Add(-50*24*time.Hour), 10)

	since := now.Add(-30 * 24 * time.Hour)
	stats, err := st.DeploymentStatsAll(since)
	if err != nil {
		t.Fatalf("stats all: %v", err)
	}
	if stats.Total != 4 {
		t.Fatalf("total = %d, want 4", stats.Total)
	}
	if stats.Succeeded != 3 {
		t.Fatalf("succeeded = %d, want 3", stats.Succeeded)
	}
	if got := stats.AvgBuildSec; math.Abs(got-35) > 0.01 {
		t.Fatalf("avg build sec = %v, want ~35", got)
	}
}

// TestDeploymentStatsPerApp orders most-deployed first and computes each
// app's own success rate and build time.
func TestDeploymentStatsPerApp(t *testing.T) {
	st, a1, a2 := newStatsStore(t)
	now := time.Now().UTC()

	seedDeployment(t, st, a1.ID, "deploy", "running", now, 60)
	seedDeployment(t, st, a1.ID, "deploy", "running", now, 30)
	seedDeployment(t, st, a1.ID, "deploy", "failed", now, 0)
	seedDeployment(t, st, a2.ID, "deploy", "running", now, 15)

	perApp, err := st.DeploymentStatsPerApp(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("stats per app: %v", err)
	}
	if len(perApp) != 2 {
		t.Fatalf("rows = %d, want 2", len(perApp))
	}
	if perApp[0].Slug != "alpha" || perApp[0].Total != 3 || perApp[0].Succeeded != 2 {
		t.Fatalf("alpha row = %+v, want slug alpha total 3 succeeded 2", perApp[0])
	}
	if got := perApp[0].AvgBuildSec; math.Abs(got-45) > 0.01 {
		t.Fatalf("alpha avg build sec = %v, want ~45", got)
	}
	if perApp[1].Slug != "beta" || perApp[1].Total != 1 || perApp[1].Succeeded != 1 {
		t.Fatalf("beta row = %+v, want slug beta total 1 succeeded 1", perApp[1])
	}
}

// TestDeploysPerDay buckets by UTC day, oldest first, window-excluding.
func TestDeploysPerDay(t *testing.T) {
	st, a1, a2 := newStatsStore(t)
	now := time.Now().UTC()

	seedDeployment(t, st, a1.ID, "deploy", "running", now, 10)
	seedDeployment(t, st, a2.ID, "deploy", "failed", now, 0)
	seedDeployment(t, st, a1.ID, "deploy", "running", now.Add(-24*time.Hour), 10)
	seedDeployment(t, st, a1.ID, "deploy", "running", now.Add(-50*24*time.Hour), 10)

	days, err := st.DeploysPerDay(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("deploys per day: %v", err)
	}
	if len(days) != 2 {
		t.Fatalf("days = %d, want 2", len(days))
	}
	if days[0].Count != 1 || days[1].Count != 2 {
		t.Fatalf("day counts = %+v, want [1 2] (oldest first)", days)
	}
	if days[1].Day != now.Format("2006-01-02") {
		t.Fatalf("newest bucket day = %q, want %q", days[1].Day, now.Format("2006-01-02"))
	}
}
