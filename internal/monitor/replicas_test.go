package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/appspec"
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
	m.SetHealer(func(_ context.Context, _ store.App, sl appspec.Slot) (bool, error) {
		healed = append(healed, sl.Slot)
		return false, nil
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
