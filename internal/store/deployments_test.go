package store

import (
	"path/filepath"
	"testing"
)

// TestDeploymentTriggerRoundTrip proves the trigger column survives create →
// read across every deployment query path.
func TestDeploymentTriggerRoundTrip(t *testing.T) {
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
	app, err := st.CreateApp(App{ProjectID: proj.ID, Name: "A", Slug: "a"})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	d, err := st.CreateDeployment(Deployment{AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "webhook"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.GetDeployment(d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Trigger != "webhook" {
		t.Fatalf("GetDeployment trigger = %q, want webhook", got.Trigger)
	}

	list, err := st.ListDeployments(app.ID, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Trigger != "webhook" {
		t.Fatalf("ListDeployments trigger = %+v, want webhook row", list)
	}

	latest, err := st.LatestDeploymentOfKind(app.ID, "deploy")
	if err != nil || latest == nil {
		t.Fatalf("latest: %v (nil: %v)", err, latest == nil)
	}
	if latest.Trigger != "webhook" {
		t.Fatalf("LatestDeploymentOfKind trigger = %q, want webhook", latest.Trigger)
	}

	// ClaimNextQueued re-reads via GetDeployment — the trigger must survive.
	if _, err := st.ClaimNextQueued(); err != nil {
		t.Fatalf("claim: %v", err)
	}
	claimed, err := st.GetDeployment(d.ID)
	if err != nil {
		t.Fatalf("get claimed: %v", err)
	}
	if claimed.Trigger != "webhook" {
		t.Fatalf("claimed trigger = %q, want webhook", claimed.Trigger)
	}
}

// TestDeploymentTriggerDefaultsEmpty proves legacy rows (no trigger value)
// read back as '' so the UI falls back to kind.
func TestDeploymentTriggerDefaultsEmpty(t *testing.T) {
	st, d := newTestStore(t) // CreateDeployment without a trigger
	got, err := st.GetDeployment(d.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Trigger != "" {
		t.Fatalf("trigger = %q, want '' for legacy rows", got.Trigger)
	}
}
