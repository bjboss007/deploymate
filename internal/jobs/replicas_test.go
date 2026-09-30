package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func indexOf(ops []string, want string) int {
	for i, o := range ops {
		if o == want {
			return i
		}
	}
	return -1
}

func setReplicas(t *testing.T, st *store.Store, appID string, n int) store.App {
	t.Helper()
	if err := st.UpdateAppReplicas(appID, n); err != nil {
		t.Fatalf("set replicas: %v", err)
	}
	app, err := st.GetAppByID(appID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	return app
}

func seedSlots(t *testing.T, st *store.Store, app store.App, n int, deployID string) {
	t.Helper()
	for slot := 1; slot <= n; slot++ {
		if err := st.UpsertAppReplica(store.AppReplica{
			AppID: app.ID, Slot: slot, ContainerName: appspec.SlotName(app.Slug, slot), HostPort: 40000 + slot, DeployID: deployID,
		}); err != nil {
			t.Fatalf("seed slot %d: %v", slot, err)
		}
	}
}

// TestRolloutReplacesSlotsOneAtATime is the rollout floor: with 3 replicas
// each slot's staged container is started (and probed) before that slot's
// old container is removed, and slot i is fully swapped before slot i+1's
// staged container even starts — so at most one slot is ever in flight and
// at least N-1 serve throughout. Every slot records the new deployment.
func TestRolloutReplacesSlotsOneAtATime(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := setReplicas(t, st, seedApp(t, st).ID, 3)
	seedSlots(t, st, app, 3, "old")
	d := queuedManual(t, st, app.ID, "nginx:alpine")
	fake.has = true

	w.process(context.Background(), d)

	if got, _ := st.GetDeployment(d.ID); got.Status != "running" {
		t.Fatalf("deployment status = %q (%s), want running", got.Status, got.Error)
	}
	ops := fake.opLog()
	for slot := 1; slot <= 3; slot++ {
		staged := appspec.StagedSlotName("web", slot, d.ID)
		canon := appspec.SlotName("web", slot)
		start, remove, rename := indexOf(ops, "start "+staged), indexOf(ops, "remove "+canon), indexOf(ops, "rename "+staged+" "+canon)
		if start < 0 || remove < 0 || rename < 0 || !(start < remove && remove < rename) {
			t.Fatalf("slot %d: start=%d remove=%d rename=%d — old must go only after the staged one is up\n%s",
				slot, start, remove, rename, strings.Join(ops, "\n"))
		}
		if slot < 3 {
			next := indexOf(ops, "start "+appspec.StagedSlotName("web", slot+1, d.ID))
			if next < rename {
				t.Errorf("slot %d staged before slot %d finished swapping — two slots in flight", slot+1, slot)
			}
		}
	}
	rows, _ := st.ListAppReplicas(app.ID)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for _, r := range rows {
		if r.DeployID != d.ID || r.HostPort == 40000+r.Slot {
			t.Errorf("slot %d = deploy %q port %d, want the new deployment on a fresh port", r.Slot, r.DeployID, r.HostPort)
		}
	}
	// Distinct routers per slot, one shared service (see internal/proxy).
	specs := fake.createdSpecs()
	if len(specs) != 3 || specs[0].Labels["deploymate.slot"] != "1" || specs[2].Labels["deploymate.slot"] != "3" {
		t.Errorf("created specs = %d, slot labels wrong", len(specs))
	}
	app2, _ := st.GetAppByID(app.ID)
	if app2.PreviewHostPort != rows[0].HostPort {
		t.Errorf("preview_host_port = %d, want slot 1's port %d (dual-write)", app2.PreviewHostPort, rows[0].HostPort)
	}
}

// TestRolloutHaltsOnFailedSlot: slot 2's staged container fails its probe →
// the rollout stops, slot 1 keeps the new image, slots 2-3 keep the old one
// (drift, surfaced per slot), the staged container is removed, and the app
// is not marked failed because replicas still run.
func TestRolloutHaltsOnFailedSlot(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := setReplicas(t, st, seedApp(t, st).ID, 3)
	seedSlots(t, st, app, 3, "old")
	fake.has = true
	fake.info = runtime.Info{Running: true}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(ok.Close)
	var calls atomic.Int32
	w.probeURL = func(int) string {
		if calls.Add(1) == 2 {
			return "http://127.0.0.1:1/" // slot 2: closed port, probe fails
		}
		return ok.URL
	}
	d := queuedManual(t, st, app.ID, "nginx:alpine")

	w.process(context.Background(), d)

	if got, _ := st.GetDeployment(d.ID); got.Status != "failed" {
		t.Fatalf("deployment = %q, want failed", got.Status)
	}
	rows, _ := st.ListAppReplicas(app.ID)
	want := map[int]string{1: d.ID, 2: "old", 3: "old"}
	for _, r := range rows {
		if r.DeployID != want[r.Slot] {
			t.Errorf("slot %d deploy = %q, want %q", r.Slot, r.DeployID, want[r.Slot])
		}
	}
	ops := fake.opLog()
	if indexOf(ops, "remove "+appspec.StagedSlotName("web", 2, d.ID)) < 0 {
		t.Error("failed staged container must be removed")
	}
	if indexOf(ops, "remove dm-web-r2") >= 0 || indexOf(ops, "start "+appspec.StagedSlotName("web", 3, d.ID)) >= 0 {
		t.Errorf("rollout must halt at slot 2 without touching old slot 2 or starting slot 3:\n%s", strings.Join(ops, "\n"))
	}
	if a, _ := st.GetAppByID(app.ID); a.Status == "failed" {
		t.Error("app must not be marked failed while replicas still run")
	}
	lines, _ := st.ListBuildLogs(d.ID, 0)
	if !strings.Contains(strings.Join(lines, "\n"), "rollout halted") {
		t.Errorf("build log should explain the halt:\n%s", strings.Join(lines, "\n"))
	}
}

// TestScaleUpAndDown: a `scale` deployment starts the missing slots from the
// current image without touching slot 1 or current_deployment_id, and a
// scale-down removes the highest slots and their rows.
func TestScaleUpAndDown(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	fake.info = runtime.Info{Running: true}
	cur, err := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "manual", Status: "running", ImageTag: "nginx:1", CommitSHA: "abc123"})
	if err != nil {
		t.Fatalf("current deployment: %v", err)
	}
	if err := st.SetAppCurrentDeployment(app.ID, cur.ID); err != nil {
		t.Fatalf("set current: %v", err)
	}
	app = setReplicas(t, st, app.ID, 3)
	// Legacy shape: no replica rows yet — the scale records slot 1 itself.

	up, _ := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "scale", Status: "queued", Trigger: "scale", ImageTag: "nginx:1"})
	w.process(context.Background(), up)

	if got, _ := st.GetDeployment(up.ID); got.Status != "running" {
		t.Fatalf("scale-up = %q (%s), want running", got.Status, got.Error)
	}
	rows, _ := st.ListAppReplicas(app.ID)
	if len(rows) != 3 {
		t.Fatalf("rows after scale-up = %d, want 3: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.DeployID != cur.ID {
			t.Errorf("slot %d deploy = %q, want the current deployment %q (no drift from a scale)", r.Slot, r.DeployID, cur.ID)
		}
	}
	specs := fake.createdSpecs()
	if len(specs) != 2 || specs[0].Name != appspec.StagedSlotName("web", 2, up.ID) || specs[0].Image != "nginx:1" {
		t.Fatalf("scale-up created %+v, want slots 2 and 3 from nginx:1", specs)
	}
	if !strings.Contains(strings.Join(specs[0].Env, " "), "GIT_SHA=abc123") {
		t.Error("scale slots must carry the current deployment's GIT_SHA")
	}
	if indexOf(fake.opLog(), "remove dm-web") >= 0 {
		t.Error("scale-up must never touch slot 1")
	}
	if a, _ := st.GetAppByID(app.ID); a.CurrentDeploymentID != cur.ID {
		t.Errorf("current_deployment_id = %q, want unchanged %q", a.CurrentDeploymentID, cur.ID)
	}

	app = setReplicas(t, st, app.ID, 1)
	down, _ := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "scale", Status: "queued", Trigger: "scale", ImageTag: "nginx:1"})
	w.process(context.Background(), down)
	rows, _ = st.ListAppReplicas(app.ID)
	if len(rows) != 1 || rows[0].Slot != 1 {
		t.Errorf("rows after scale-down = %+v, want slot 1 only", rows)
	}
	ops := fake.opLog()
	if indexOf(ops, "remove dm-web-r2") < 0 || indexOf(ops, "remove dm-web-r3") < 0 {
		t.Errorf("scale-down must remove r2 and r3:\n%s", strings.Join(ops, "\n"))
	}
}

// TestDeployConvergesReplicaSet: a deploy after the count dropped removes
// the extra slots it no longer rolls.
func TestDeployConvergesReplicaSet(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := setReplicas(t, st, seedApp(t, st).ID, 1)
	seedSlots(t, st, app, 3, "old")
	fake.has = true
	d := queuedManual(t, st, app.ID, "nginx:alpine")
	w.process(context.Background(), d)
	rows, _ := st.ListAppReplicas(app.ID)
	if len(rows) != 1 || rows[0].DeployID != d.ID {
		t.Errorf("rows = %+v, want slot 1 on the new deployment only", rows)
	}
	if indexOf(fake.opLog(), "remove dm-web-r3") < 0 {
		t.Error("deploy must remove slots above the desired count")
	}
}
