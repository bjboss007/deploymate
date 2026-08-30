package store

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStore opens a fresh migrated store with an app + deployment, ready
// for build-log tests.
func newTestStore(t *testing.T) (*Store, Deployment) {
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
	app, err := st.CreateApp(App{ProjectID: proj.ID, Name: "A", Slug: "a", Status: "running"})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	d, err := st.CreateDeployment(Deployment{AppID: app.ID, Kind: "deploy", Status: "running"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return st, d
}

func TestPruneBuildLogsBefore(t *testing.T) {
	st, d := newTestStore(t)

	for _, line := range []string{"one", "two", "three"} {
		if err := st.AppendBuildLog(d.ID, "stdout", line); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	// A cutoff before every row's timestamp prunes nothing.
	if n, err := st.PruneBuildLogsBefore(time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("prune (past cutoff): %v", err)
	} else if n != 0 {
		t.Fatalf("past cutoff pruned %d rows, want 0", n)
	}
	lines, err := st.ListBuildLogs(d.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("after past-cutoff prune: %d lines, want 3", len(lines))
	}

	// A cutoff after every row's timestamp prunes all of them.
	if n, err := st.PruneBuildLogsBefore(time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("prune (future cutoff): %v", err)
	} else if n != 3 {
		t.Fatalf("future cutoff pruned %d rows, want 3", n)
	}
	lines, err = st.ListBuildLogs(d.ID, 0)
	if err != nil {
		t.Fatalf("list after prune: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("after future-cutoff prune: %d lines, want 0", len(lines))
	}
}
