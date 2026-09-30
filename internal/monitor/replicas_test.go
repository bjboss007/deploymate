package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func seedTwoSlots(t *testing.T, st *store.Store, app store.App) {
	t.Helper()
	for slot := 1; slot <= 2; slot++ {
		if err := st.UpsertAppReplica(store.AppReplica{AppID: app.ID, Slot: slot, ContainerName: appspec.SlotName(app.Slug, slot), HostPort: 1}); err != nil {
			t.Fatalf("seed slot %d: %v", slot, err)
		}
	}
}

// TestAnyReplicaUpIsHealthy: slot 1 answers, slot 2 is dead → the app is
// healthy (decision 1: any replica up), and each slot's verdict is stored
// so the preview proxy can skip the dead one.
func TestAnyReplicaUpIsHealthy(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(up.Close)
	m.probeURLFn = func(_ string, slot int) string {
		if slot == 1 {
			return up.URL
		}
		return "http://127.0.0.1:1/"
	}
	// Two consecutive OK rounds recover an unhealthy app (hysteresis).
	m.probeAppHealth(context.Background(), app)
	app, _ = st.GetAppByID(app.ID)
	m.probeAppHealth(context.Background(), app)

	got, _ := st.GetAppByID(app.ID)
	if got.Health != "healthy" {
		t.Errorf("health = %q with one replica up, want healthy", got.Health)
	}
	rows, _ := st.ListAppReplicas(app.ID)
	if rows[0].Status != "healthy" || rows[1].Status != "unhealthy" {
		t.Errorf("slot verdicts = %q/%q, want healthy/unhealthy", rows[0].Status, rows[1].Status)
	}
}

// TestAllReplicasDownIsUnhealthy: no replica answers → unhealthy after the
// usual three failed rounds.
func TestAllReplicasDownIsUnhealthy(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	if err := st.UpdateAppHealth(app.ID, "healthy"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < unhealthyAfterFails; i++ {
		a, _ := st.GetAppByID(app.ID)
		m.probeAppHealth(context.Background(), a)
	}
	if got, _ := st.GetAppByID(app.ID); got.Health != "unhealthy" {
		t.Errorf("health = %q with every replica down, want unhealthy", got.Health)
	}
}

// TestHealWaitsForRollout is the heal/rollout mutex: while a deployment is
// queued/building, failing slots are NOT healed (they are being replaced on
// purpose); once it is terminal, each slot heals on its own cooldown.
func TestHealWaitsForRollout(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	var healed []int
	m.SetHealer(func(_ context.Context, _ store.App, sl appspec.Slot) (string, error) {
		healed = append(healed, sl.Slot)
		return "", nil
	})
	d, err := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "deploy", Status: "building"})
	if err != nil {
		t.Fatal(err)
	}
	m.probeAppHealth(context.Background(), app)
	if len(healed) != 0 {
		t.Fatalf("healed %v mid-rollout, want none", healed)
	}

	d.Status = "running"
	if err := st.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	m.probeAppHealth(context.Background(), app)
	if len(healed) != 2 || healed[0] != 1 || healed[1] != 2 {
		t.Errorf("healed %v after the rollout, want [1 2] (per-slot cooldowns)", healed)
	}
}

// slotRuntime answers per container name (limits, restarts, stats).
type slotRuntime struct {
	stubRuntime
	info  map[string]runtime.Info
	stats map[string]runtime.Stats
}

func (r *slotRuntime) Inspect(_ context.Context, name string) (runtime.Info, error) {
	info, ok := r.info[name]
	if !ok {
		return runtime.Info{}, runtime.ErrContainerNotFound
	}
	return info, nil
}

func (r *slotRuntime) Stats(_ context.Context, name string) (runtime.Stats, error) {
	return r.stats[name], nil
}

func seedSlotMetrics(t *testing.T, st *store.Store, appID string, slot int, memMB uint64, cpu float64, n int, age time.Duration) {
	t.Helper()
	base := time.Now().UTC().Add(-age)
	for i := 0; i < n; i++ {
		if err := st.InsertMetric(appID, store.Metric{
			Slot: slot, TS: base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			CPUPercent: cpu, MemBytes: memMB << 20,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSampleAllPerReplica: one sample per replica per tick, same timestamp.
func TestSampleAllPerReplica(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	m.rt = &slotRuntime{stats: map[string]runtime.Stats{
		"dm-py-api": {MemBytes: 10 << 20}, "dm-py-api-r2": {MemBytes: 20 << 20},
	}}
	m.sampleAll(context.Background())
	total, _ := st.ListMetrics(app.ID, 10)
	if len(total) != 1 || total[0].MemBytes != 30<<20 {
		t.Fatalf("one tick totalling both replicas expected, got %+v", total)
	}
	if r2, _ := st.ListSlotMetrics(app.ID, 2, 10); len(r2) != 1 || r2[0].MemBytes != 20<<20 {
		t.Errorf("slot 2 sample = %+v", r2)
	}
}

// TestDetectResourcesUsesBusiestReplica: limits are per container, so they
// are sized for the hottest replica, not an average that squeezes it.
func TestDetectResourcesUsesBusiestReplica(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	seedSlotMetrics(t, st, app.ID, 1, 100, 10, 20, time.Hour)
	seedSlotMetrics(t, st, app.ID, 2, 400, 60, 20, time.Hour)
	m.detectResources(context.Background())
	got, _ := st.GetAppByID(app.ID)
	if got.MemLimitMB != 800 || got.CPULimit != 1.2 {
		t.Errorf("limits = %d MB / %v CPU, want 800 MB / 1.2 (2× the busiest replica)", got.MemLimitMB, got.CPULimit)
	}
}

// TestPressureOnAnyReplicaResizes: replica 2 alone past 80% of its limit
// queues the resize (a resize rolls every replica).
func TestPressureOnAnyReplicaResizes(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	m.rt = &slotRuntime{info: map[string]runtime.Info{
		"dm-py-api":    {Running: true, MemLimitMB: 256, CPULimit: 1, Image: "py:1"},
		"dm-py-api-r2": {Running: true, MemLimitMB: 256, CPULimit: 1, Image: "py:1"},
	}}
	seedSlotMetrics(t, st, app.ID, 1, 50, 5, 20, time.Minute)
	seedSlotMetrics(t, st, app.ID, 2, 240, 5, 20, time.Minute) // 94% of 256 MB
	m.checkResourcePressure(context.Background(), app)
	d, err := st.LatestDeploymentOfKind(app.ID, "resize")
	if err != nil || d == nil {
		t.Fatalf("replica 2's pressure must queue a resize (err %v)", err)
	}
	if got, _ := st.GetAppByID(app.ID); got.MemLimitMB != 480 {
		t.Errorf("new limit = %d MB, want 480 (2× the hot replica's P90)", got.MemLimitMB)
	}
}

// TestRestartsTrackedPerReplica: each slot's restart count is tracked on
// its own key, so replica 2 crash-looping is noticed.
func TestRestartsTrackedPerReplica(t *testing.T) {
	m, st, app := newTestMonitor(t)
	seedTwoSlots(t, st, app)
	rt := &slotRuntime{info: map[string]runtime.Info{
		"dm-py-api": {Running: true, Restarts: 0}, "dm-py-api-r2": {Running: true, Restarts: 0},
	}}
	m.rt = rt
	m.checkRestarts(context.Background(), app)
	rt.info["dm-py-api-r2"] = runtime.Info{Running: true, Restarts: 3}
	m.checkRestarts(context.Background(), app)
	if _, alerted := m.restartAlertAt[healKey(app.ID, 2)]; !alerted {
		t.Error("replica 2's restarts must alert")
	}
	if _, alerted := m.restartAlertAt[app.ID]; alerted {
		t.Error("replica 1 did not restart and must not alert")
	}
}
