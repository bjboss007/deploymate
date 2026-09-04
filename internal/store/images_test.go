package store

import (
	"path/filepath"
	"testing"
)

// TestTotalImageBytes proves the rollback-registry disk sum: 0 on an empty
// store, and the sum of recorded sizes (the /stats "tracked app images"
// card).
func TestTotalImageBytes(t *testing.T) {
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

	total, err := st.TotalImageBytes()
	if err != nil {
		t.Fatalf("empty total: %v", err)
	}
	if total != 0 {
		t.Fatalf("empty total = %d, want 0", total)
	}

	for _, sz := range []int64{123456, 789} {
		if _, err := st.CreateImage(Image{AppID: app.ID, Tag: "img", DeploymentID: "d", SizeBytes: sz}); err != nil {
			t.Fatalf("create image: %v", err)
		}
	}
	total, err = st.TotalImageBytes()
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if total != 124245 {
		t.Fatalf("total = %d, want 124245", total)
	}
}

// TestListAllServicesRoundTrip proves services are listed across projects
// with their volume names (the stats panel matches daemon volumes against
// them).
func TestListAllServicesRoundTrip(t *testing.T) {
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
	if _, err := st.CreateService(Service{
		ProjectID: proj.ID, Type: "postgres", Name: "postgres", Slug: "postgres",
		Image: "postgres:16-alpine", VolumeName: "dm-svc-postgres-data", Port: 5432,
	}); err != nil {
		t.Fatalf("create service: %v", err)
	}

	svcs, err := st.ListAllServices()
	if err != nil {
		t.Fatalf("list all services: %v", err)
	}
	if len(svcs) != 1 {
		t.Fatalf("got %d services, want 1", len(svcs))
	}
	if svcs[0].VolumeName != "dm-svc-postgres-data" {
		t.Errorf("volume name = %q, want dm-svc-postgres-data", svcs[0].VolumeName)
	}
}
