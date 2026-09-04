package store

import (
	"path/filepath"
	"testing"
)

// TestAppDeployConfigRoundTrip proves the deploy-form columns persist and
// round-trip: entrypoint/command through CreateApp, and the full
// image/port/entrypoint/command update.
func TestAppDeployConfigRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	owner, err := st.CreateUser(User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	app, err := st.CreateApp(App{
		ProjectID: proj.ID, Name: "Web", Slug: "web",
		Entrypoint: "/bin/sh", Command: "-c echo hi",
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	got, err := st.GetAppBySlug("web")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Entrypoint != "/bin/sh" || got.Command != "-c echo hi" {
		t.Errorf("create round-trip = %q / %q, want /bin/sh / -c echo hi", got.Entrypoint, got.Command)
	}

	if err := st.UpdateAppDeployConfig(app.ID, "nginx:alpine", 80, "sleep", "3000"); err != nil {
		t.Fatalf("update deploy config: %v", err)
	}
	got, err = st.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Image != "nginx:alpine" || got.Port != 80 || got.Entrypoint != "sleep" || got.Command != "3000" {
		t.Errorf("update round-trip = %q/%d/%q/%q, want nginx:alpine/80/sleep/3000",
			got.Image, got.Port, got.Entrypoint, got.Command)
	}

	// Clearing: empty strings are the "image default" signal, persisted as
	// such (never resurrected from a previous deploy).
	if err := st.UpdateAppDeployConfig(app.ID, "nginx:alpine", 80, "", ""); err != nil {
		t.Fatalf("clear overrides: %v", err)
	}
	got, _ = st.GetAppByID(app.ID)
	if got.Entrypoint != "" || got.Command != "" {
		t.Errorf("cleared overrides = %q / %q, want empty", got.Entrypoint, got.Command)
	}
}

// TestAppDefaultsToNoOverrides proves freshly created apps (and legacy rows
// created before migration 0013) carry empty overrides.
func TestAppDefaultsToNoOverrides(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	owner, err := st.CreateUser(User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(App{ProjectID: proj.ID, Name: "Web", Slug: "web"})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if app.Entrypoint != "" || app.Command != "" {
		t.Errorf("fresh app overrides = %q / %q, want empty", app.Entrypoint, app.Command)
	}
}
