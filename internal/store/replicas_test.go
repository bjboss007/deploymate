package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func newReplicaTestApp(t *testing.T) (*Store, App) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	proj, err := st.CreateProject(Project{UserID: owner.ID, Name: "P", Slug: "p"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	app, err := st.CreateApp(App{ProjectID: proj.ID, Name: "Web", Slug: "web", Port: 8080})
	if err != nil {
		t.Fatalf("app: %v", err)
	}
	return st, app
}

// TestAppReplicaDefaults: a new app is one replica probed at "/", and the
// columns round-trip through every reader.
func TestAppReplicaDefaults(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if app.Replicas != 1 || app.HealthPath != "/" {
		t.Fatalf("create defaults = replicas %d path %q, want 1 and /", app.Replicas, app.HealthPath)
	}
	got, err := st.GetAppBySlug("web")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Replicas != 1 || got.HealthPath != "/" {
		t.Errorf("read back = replicas %d path %q", got.Replicas, got.HealthPath)
	}
}

// TestUpdateAppReplicasCap: 1..5 accepted, anything else rejected without
// touching the stored count (the cap is a guard, not a clamp).
func TestUpdateAppReplicasCap(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if err := st.UpdateAppReplicas(app.ID, 3); err != nil {
		t.Fatalf("set 3: %v", err)
	}
	for _, bad := range []int{0, -1, 6, 100} {
		if err := st.UpdateAppReplicas(app.ID, bad); !errors.Is(err, ErrReplicasOutOfRange) {
			t.Errorf("UpdateAppReplicas(%d) err = %v, want ErrReplicasOutOfRange", bad, err)
		}
	}
	got, _ := st.GetAppByID(app.ID)
	if got.Replicas != 3 {
		t.Errorf("replicas = %d after rejected updates, want 3", got.Replicas)
	}
	if ClampReplicas(0) != 1 || ClampReplicas(9) != MaxReplicas || ClampReplicas(2) != 2 {
		t.Error("ClampReplicas must map into 1..MaxReplicas")
	}
}

// TestAppReplicaRows: upsert replaces by slot (and resets the probe
// verdict), status updates stick, rows list in slot order, and scale-down
// deletes only the slots above the kept count.
func TestAppReplicaRows(t *testing.T) {
	st, app := newReplicaTestApp(t)
	for slot, port := range map[int]int{2: 20002, 1: 20001, 3: 20003} {
		if err := st.UpsertAppReplica(AppReplica{AppID: app.ID, Slot: slot, ContainerName: "c", HostPort: port, DeployID: "d1"}); err != nil {
			t.Fatalf("upsert %d: %v", slot, err)
		}
	}
	if err := st.SetAppReplicaStatus(app.ID, 2, "unhealthy"); err != nil {
		t.Fatalf("status: %v", err)
	}
	rows, err := st.ListAppReplicas(app.ID)
	if err != nil || len(rows) != 3 {
		t.Fatalf("list = %d rows, err %v; want 3", len(rows), err)
	}
	for i, r := range rows {
		if r.Slot != i+1 {
			t.Fatalf("rows not in slot order: %+v", rows)
		}
	}
	if rows[1].Status != "unhealthy" {
		t.Errorf("slot 2 status = %q, want unhealthy", rows[1].Status)
	}

	// A swap re-records slot 2: new port + deployment, verdict reset.
	if err := st.UpsertAppReplica(AppReplica{AppID: app.ID, Slot: 2, ContainerName: "c", HostPort: 30002, DeployID: "d2"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	rows, _ = st.ListAppReplicas(app.ID)
	if len(rows) != 3 || rows[1].HostPort != 30002 || rows[1].DeployID != "d2" || rows[1].Status != "" {
		t.Errorf("slot 2 after re-upsert = %+v", rows[1])
	}

	if err := st.DeleteAppReplicasAbove(app.ID, 1); err != nil {
		t.Fatalf("delete above: %v", err)
	}
	rows, _ = st.ListAppReplicas(app.ID)
	if len(rows) != 1 || rows[0].Slot != 1 {
		t.Errorf("after scale-down to 1: %+v", rows)
	}

	// Rows cascade with the app.
	if err := st.DeleteApp(app.ID); err != nil {
		t.Fatalf("delete app: %v", err)
	}
	if rows, _ = st.ListAppReplicas(app.ID); len(rows) != 0 {
		t.Errorf("replica rows survived app delete: %+v", rows)
	}
}

// TestHasActiveDeployment backs the monitor's heal/rollout mutex.
func TestHasActiveDeployment(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if busy, err := st.HasActiveDeployment(app.ID); err != nil || busy {
		t.Fatalf("no deployments: busy=%v err=%v", busy, err)
	}
	d, err := st.CreateDeployment(Deployment{AppID: app.ID, Kind: "deploy", Status: "queued"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if busy, _ := st.HasActiveDeployment(app.ID); !busy {
		t.Error("queued deployment must count as active")
	}
	d.Status = "running"
	if err := st.UpdateDeployment(d); err != nil {
		t.Fatalf("update: %v", err)
	}
	if busy, _ := st.HasActiveDeployment(app.ID); busy {
		t.Error("terminal deployment must not count as active")
	}
}
