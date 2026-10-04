package monitor

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/tlscheck"
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
func (stubRuntime) Exec(context.Context, string, []string) (string, error)                { return "", nil }
func (stubRuntime) ExecEnv(context.Context, string, []string, []string) (string, error)   { return "", nil }
func (stubRuntime) WriteFile(context.Context, string, string, []byte) error               { return nil }
func (stubRuntime) ReadFile(context.Context, string, string) ([]byte, error)              { return nil, nil }
func (stubRuntime) Stats(context.Context, string) (runtime.Stats, error)    { return runtime.Stats{}, nil }
func (stubRuntime) StorageUsed(context.Context) (uint64, error)             { return 0, nil }
func (stubRuntime) DiskUsage(context.Context) (runtime.DiskUsage, error)        { return runtime.DiskUsage{}, nil }
func (stubRuntime) ImageSize(context.Context, string) (uint64, error)           { return 0, nil }
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
	m.probeURLFn = func(string, int) string { return "http://127.0.0.1:1/" }
	return m, st, app
}

func TestAutoHealBindinglessContainer(t *testing.T) {
	m, st, app := newTestMonitor(t)
	calls := 0
	m.SetHealer(func(ctx context.Context, a store.App, _ appspec.Slot) (string, error) {
		calls++
		return "recreated its container to restore the preview port binding", nil
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
	m.SetHealer(func(ctx context.Context, a store.App, _ appspec.Slot) (string, error) {
		return "", nil
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
	m.SetHealer(func(ctx context.Context, a store.App, _ appspec.Slot) (string, error) {
		calls++
		return "", errors.New("docker unavailable")
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

// The monitor records a domain's real certificate state, re-checks a
// not-yet-active domain quickly but an active one rarely, and writes nothing
// when nothing changed.
func TestCheckTLSRecordsAndThrottles(t *testing.T) {
	m, st, app := newTestMonitor(t)
	d, err := st.CreateDomain(store.Domain{AppID: app.ID, Hostname: "app.example.com", TLSStatus: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	expiry := time.Now().Add(60 * 24 * time.Hour).UTC().Truncate(time.Second)
	m.tlsFn = func(context.Context, string) tlscheck.Result {
		calls++
		return tlscheck.Result{Status: tlscheck.Active, Expires: expiry, Detail: "ok"}
	}
	get := func() store.Domain {
		ds, _ := st.ListDomains(app.ID)
		return ds[0]
	}

	m.checkTLS(context.Background(), d)
	got := get()
	if got.TLSStatus != "active" || got.CertExpiresAt != expiry.Format(time.RFC3339) {
		t.Fatalf("domain = %q / %q, want active with the expiry", got.TLSStatus, got.CertExpiresAt)
	}
	// Immediately again: throttled (active domains are re-checked every 30 min).
	m.checkTLS(context.Background(), got)
	if calls != 1 {
		t.Errorf("handshakes = %d, want 1 (throttled)", calls)
	}
	// A pending domain is re-checked after the short interval.
	m.mu.Lock()
	m.lastTLS[d.ID] = time.Now().Add(-3 * time.Minute)
	m.mu.Unlock()
	pending := got
	pending.TLSStatus = "pending"
	m.checkTLS(context.Background(), pending)
	if calls != 2 {
		t.Errorf("a pending domain should be re-checked after 2 minutes (calls=%d)", calls)
	}
}
