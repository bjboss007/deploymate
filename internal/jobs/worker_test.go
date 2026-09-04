package jobs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeRT is a scriptable runtime for worker tests: it records created specs
// and pulls, and lets each test stub behavior via hooks/fields.
type fakeRT struct {
	mu      sync.Mutex
	created []runtime.Spec
	pulled  []string
	pullFn  func(ctx context.Context, image string) error // nil = success
	has     bool                                          // HasImage result
	info    runtime.Info
	infoErr error
}

func (f *fakeRT) EnsureNetwork(context.Context, string) error { return nil }
func (f *fakeRT) PullImage(ctx context.Context, image string) error {
	f.mu.Lock()
	f.pulled = append(f.pulled, image)
	fn := f.pullFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, image)
	}
	return nil
}
func (f *fakeRT) HasImage(context.Context, string) (bool, error) { return f.has, nil }
func (f *fakeRT) RemoveImage(context.Context, string) (bool, error) {
	return true, nil
}
func (f *fakeRT) Create(_ context.Context, spec runtime.Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, spec)
	return "cid", nil
}
func (f *fakeRT) Start(context.Context, string) error          { return nil }
func (f *fakeRT) Stop(context.Context, string, int) error      { return nil }
func (f *fakeRT) Remove(context.Context, string) error         { return nil }
func (f *fakeRT) Rename(context.Context, string, string) error { return nil }
func (f *fakeRT) Inspect(context.Context, string) (runtime.Info, error) {
	return f.info, f.infoErr
}
func (f *fakeRT) Logs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeRT) Exec(context.Context, string, []string) (string, error) { return "", nil }
func (f *fakeRT) Stats(context.Context, string) (runtime.Stats, error)    { return runtime.Stats{}, nil }
func (f *fakeRT) StorageUsed(context.Context) (uint64, error)             { return 0, nil }
func (f *fakeRT) Close() error                                            { return nil }

func (f *fakeRT) pullNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pulled...)
}

func (f *fakeRT) createdSpecs() []runtime.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtime.Spec(nil), f.created...)
}

// newTestWorker builds a Worker with everything stubbed: a temp SQLite store
// (auto-migrated), a fake runtime, and a probe override so swaps probe a
// live 200 server with a single instant attempt. The pull timeout is
// shortened to 50 ms for the timeout test.
func newTestWorker(t *testing.T) (*Worker, *fakeRT, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &fakeRT{}
	prov := services.NewProvisioner(st, fake, [32]byte{7}, appspec.NetworkName)
	w := NewWorker(st, fake, prov, sse.NewBroker(), [32]byte{7}, t.TempDir(),
		appspec.NetworkName, "", "", func(store.App) []string { return nil },
		alerts.New(st, [32]byte{7}))
	w.pullTimeout = 50 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	w.probeURL = func(int) string { return srv.URL }
	w.probeAttempts = 1
	w.probeInterval = time.Millisecond
	return w, fake, st
}

// seedApp creates the owner/project/app rows a deployment needs.
func seedApp(t *testing.T, st *store.Store) store.App {
	t.Helper()
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(store.App{
		ProjectID: proj.ID, Name: "Web", Slug: "web",
		Status: "running", Port: 8080, Image: "nginx:alpine",
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	return app
}

func queuedManual(t *testing.T, st *store.Store, appID, image string) store.Deployment {
	t.Helper()
	d, err := st.CreateDeployment(store.Deployment{
		AppID: appID, Kind: "manual", Status: "queued", Trigger: "manual", ImageTag: image,
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	return d
}

// TestRunManualDeploySuccess proves the full manual path: bounded pull,
// staged create + swap, and finish (deployment running + FinishedAt, app
// promoted, healthy, current_deployment_id set, build log lines recorded).
func TestRunManualDeploySuccess(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	d := queuedManual(t, st, app.ID, "nginx:alpine")

	w.process(context.Background(), d)

	got, err := st.GetDeployment(d.ID)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status = %q, want running", got.Status)
	}
	if got.FinishedAt == "" {
		t.Error("FinishedAt must be set on success")
	}
	if pulls := fake.pullNames(); len(pulls) != 1 || pulls[0] != "nginx:alpine" {
		t.Errorf("pulls = %v, want [nginx:alpine]", pulls)
	}
	created := fake.createdSpecs()
	if len(created) != 1 {
		t.Fatalf("created %d containers, want 1", len(created))
	}
	if created[0].Image != "nginx:alpine" {
		t.Errorf("created image = %q, want nginx:alpine", created[0].Image)
	}
	if want := appspec.StagedName("web", d.ID); created[0].Name != want {
		t.Errorf("created name = %q, want staged %q", created[0].Name, want)
	}
	app2, err := st.GetAppByID(app.ID)
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app2.Status != "running" || app2.Health != "healthy" {
		t.Errorf("app = status %q health %q, want running/healthy", app2.Status, app2.Health)
	}
	if app2.CurrentDeploymentID != d.ID {
		t.Errorf("current_deployment_id = %q, want %q", app2.CurrentDeploymentID, d.ID)
	}
	lines, err := st.ListBuildLogs(d.ID, 0)
	if err != nil {
		t.Fatalf("list build logs: %v", err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "pulling nginx:alpine") {
		t.Errorf("build log missing the pull line:\n%s", strings.Join(lines, "\n"))
	}
}

// TestRunManualDeployPullTimeout proves the pull is bounded: a registry that
// hangs (the fake PullImage blocks until the context dies) fails the
// deployment with a timeout error instead of blocking forever.
func TestRunManualDeployPullTimeout(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	d := queuedManual(t, st, app.ID, "nginx:alpine")

	fake.pullFn = func(ctx context.Context, image string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	w.process(context.Background(), d)

	got, _ := st.GetDeployment(d.ID)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "timed out after") {
		t.Errorf("error %q must mention the timeout", got.Error)
	}
	if created := fake.createdSpecs(); len(created) != 0 {
		t.Errorf("a timed-out pull must not create a container, created %d", len(created))
	}
	app2, _ := st.GetAppByID(app.ID)
	if app2.Status != "failed" {
		t.Errorf("app status = %q, want failed (nothing runs for it)", app2.Status)
	}
}

// TestRunManualDeployPullError proves a normal pull failure (bad tag,
// unreachable registry) fails the deployment and reports the image.
func TestRunManualDeployPullError(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	d := queuedManual(t, st, app.ID, "nginx:alpine")

	fake.pullFn = func(ctx context.Context, image string) error {
		return errors.New("registry down")
	}
	w.process(context.Background(), d)

	got, _ := st.GetDeployment(d.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "pull nginx:alpine: registry down") {
		t.Errorf("deployment = %q / %q, want failed with the pull error", got.Status, got.Error)
	}
}

// TestRunManualDeployNoImage proves a queued row without an image fails
// cleanly instead of pulling the empty string.
func TestRunManualDeployNoImage(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	d := queuedManual(t, st, app.ID, "")

	w.process(context.Background(), d)

	got, _ := st.GetDeployment(d.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "no image to deploy") {
		t.Errorf("deployment = %q / %q, want failed with the no-image error", got.Status, got.Error)
	}
	if pulls := fake.pullNames(); len(pulls) != 0 {
		t.Errorf("no pull allowed without an image, pulled %v", pulls)
	}
}

// TestRunManualDeployProbeFailureKeepsOldServing proves zero-downtime
// parity with the old handler: when the staged container fails its
// readiness probe, the deployment fails but an already-running app stays
// running (fail() only marks the app failed when nothing runs for it).
func TestRunManualDeployProbeFailureKeepsOldServing(t *testing.T) {
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	d := queuedManual(t, st, app.ID, "nginx:alpine")

	fake.has = true               // image already local — skip the pull
	fake.info = runtime.Info{Running: true} // the OLD container keeps serving
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)
	w.probeURL = func(int) string { return bad.URL }

	w.process(context.Background(), d)

	got, _ := st.GetDeployment(d.ID)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	app2, _ := st.GetAppByID(app.ID)
	if app2.Status != "running" {
		t.Errorf("app status = %q, want running (old container still serves)", app2.Status)
	}
	lines, err := st.ListBuildLogs(d.ID, 0)
	if err != nil {
		t.Fatalf("list build logs: %v", err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "still serving") {
		t.Errorf("build log missing the still-serving note:\n%s", strings.Join(lines, "\n"))
	}
}
