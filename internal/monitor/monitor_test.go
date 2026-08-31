package monitor

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// stubRuntime satisfies runtime.Runtime; the auto-heal path needs nothing
// from it (the heal function is injected), so everything is a no-op.
type stubRuntime struct{}

func (stubRuntime) EnsureNetwork(context.Context, string) error             { return nil }
func (stubRuntime) PullImage(context.Context, string) error                 { return nil }
func (stubRuntime) HasImage(context.Context, string) (bool, error)          { return true, nil }
func (stubRuntime) RemoveImage(context.Context, string) (bool, error)       { return true, nil }
func (stubRuntime) Create(context.Context, runtime.Spec) (string, error)    { return "cid", nil }
func (stubRuntime) Start(context.Context, string) error                     { return nil }
func (stubRuntime) Stop(context.Context, string, int) error                 { return nil }
func (stubRuntime) Remove(context.Context, string) error                    { return nil }
func (stubRuntime) Rename(context.Context, string, string) error             { return nil }
func (stubRuntime) Inspect(context.Context, string) (runtime.Info, error)   { return runtime.Info{}, nil }
func (stubRuntime) Logs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (stubRuntime) Exec(context.Context, string, []string) (string, error) { return "", nil }
func (stubRuntime) Stats(context.Context, string) (runtime.Stats, error)    { return runtime.Stats{}, nil }
func (stubRuntime) StorageUsed(context.Context) (uint64, error)             { return 0, nil }
func (stubRuntime) Close() error                                            { return nil }

func newTestMonitor(t *testing.T) (*Monitor, *store.Store, store.App) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(store.App{
		ProjectID: proj.ID, Name: "Py", Slug: "py-api",
		Status: "running", Port: 8000, Image: "py:latest",
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if err := st.UpdateAppHealth(app.ID, "unhealthy"); err != nil {
		t.Fatalf("mark unhealthy: %v", err)
	}
	m := New(st, stubRuntime{}, alerts.New(st, [32]byte{7}))
	// Point the probe at a port that is always closed: the default
	// preview port for the test slug could be occupied by a real
	// container on this host, which would make the probe succeed and
	// the heal path never run.
	m.probeURLFn = func(string) string { return "http://127.0.0.1:1/" }
	return m, st, app
}

func TestAutoHealBindinglessContainer(t *testing.T) {
	m, st, app := newTestMonitor(t)
	calls := 0
	m.SetHealer(func(ctx context.Context, a store.App) (bool, error) {
		calls++
		return true, nil
	})

	// The probe URL is a closed loopback port: connection refused, so the
	// heal path triggers.
	m.probeAppHealth(context.Background(), app)

	if calls != 1 {
		t.Fatalf("heal called %d times, want 1", calls)
	}
	got, err := st.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Health != "healthy" {
		t.Errorf("health = %q after heal, want healthy", got.Health)
	}
	evs, err := st.ListEvents(app.ID, 5)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == store.EventAppHealed {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no app_healed event recorded: %+v", evs)
	}

	// A second failed probe within the cooldown must not heal again.
	m.probeAppHealth(context.Background(), app)
	if calls != 1 {
		t.Errorf("heal ran %d times within cooldown, want 1", calls)
	}
}

func TestAutoHealSkipsWhenHealSucceedsWithoutRecreate(t *testing.T) {
	m, st, app := newTestMonitor(t)
	// Heal reports "no recreation needed" (container bound but broken):
	// the app must fall through to ordinary fail accounting, not flip
	// healthy.
	m.SetHealer(func(ctx context.Context, a store.App) (bool, error) {
		return false, nil
	})
	m.probeAppHealth(context.Background(), app)

	got, err := st.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Health != "unhealthy" {
		t.Errorf("health = %q, want unhealthy (no recreate, fail accounting)", got.Health)
	}
}

func TestAutoHealFailureIsLoggedAndRateLimited(t *testing.T) {
	m, st, app := newTestMonitor(t)
	calls := 0
	m.SetHealer(func(ctx context.Context, a store.App) (bool, error) {
		calls++
		return false, errors.New("docker unavailable")
	})
	m.probeAppHealth(context.Background(), app)
	got, err := st.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Health != "unhealthy" {
		t.Errorf("health = %q after failed heal, want unhealthy", got.Health)
	}
	// Stamp the attempt; the next tick must not retry for 5 minutes.
	m.probeAppHealth(context.Background(), app)
	if calls != 1 {
		t.Errorf("failed heal retried %d times within cooldown, want 1", calls)
	}
}
