package services

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeRuntime records provisioning calls and reports every image as
// present so tests never pull. Exec (readiness) succeeds unless failExec
// is set.
type fakeRuntime struct {
	created  []runtime.Spec
	started  []string
	stopped  []string
	removed  []string
	execs    int
	failExec bool
}

func (f *fakeRuntime) EnsureNetwork(ctx context.Context, name string) error { return nil }
func (f *fakeRuntime) PullImage(ctx context.Context, image string) error    { return nil }
func (f *fakeRuntime) HasImage(ctx context.Context, image string) (bool, error) {
	return true, nil
}
func (f *fakeRuntime) RemoveImage(ctx context.Context, tag string) (bool, error) { return true, nil }
func (f *fakeRuntime) Create(ctx context.Context, spec runtime.Spec) (string, error) {
	f.created = append(f.created, spec)
	return "cid-" + spec.Name, nil
}
func (f *fakeRuntime) Start(ctx context.Context, name string) error {
	f.started = append(f.started, name)
	return nil
}
func (f *fakeRuntime) Stop(ctx context.Context, name string, timeoutSec int) error {
	f.stopped = append(f.stopped, name)
	return nil
}
func (f *fakeRuntime) Remove(ctx context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}
func (f *fakeRuntime) Inspect(ctx context.Context, name string) (runtime.Info, error) {
	return runtime.Info{}, runtime.ErrContainerNotFound
}
func (f *fakeRuntime) Logs(ctx context.Context, name string, follow bool, tail int) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeRuntime) Exec(ctx context.Context, name string, cmd []string) (string, error) {
	f.execs++
	if f.failExec {
		return "", errors.New("probe failed")
	}
	return "ok", nil
}
func (f *fakeRuntime) Stats(ctx context.Context, name string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (f *fakeRuntime) StorageUsed(ctx context.Context) (uint64, error) { return 0, nil }
func (f *fakeRuntime) Close() error                                    { return nil }

func newTestProvisioner(t *testing.T) (*Provisioner, *store.Store, *fakeRuntime) {
	t.Helper()
	oldAttempts, oldInterval := readinessAttempts, readinessInterval
	readinessAttempts, readinessInterval = 3, time.Millisecond
	t.Cleanup(func() {
		readinessAttempts, readinessInterval = oldAttempts, oldInterval
	})

	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	rt := &fakeRuntime{}
	return NewProvisioner(st, rt, [32]byte{7}, "test-net"), st, rt
}

func testProject(t *testing.T, st *store.Store) store.Project {
	t.Helper()
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	p, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

func ensureOnce(t *testing.T, p *Provisioner, projectID string, types ...string) []Resolution {
	t.Helper()
	res, err := p.Ensure(context.Background(), projectID, types)
	if err != nil {
		t.Fatalf("Ensure(%v) error = %v", types, err)
	}
	return res
}

func TestEnsureCreatesMissingService(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	res := ensureOnce(t, p, proj.ID, "postgres")
	if len(res) != 1 || res[0].Action != ActionProvisioned {
		t.Fatalf("resolutions = %+v, want one provisioned", res)
	}
	if res[0].Service.Name != "postgres" || res[0].Service.Slug != "postgres" {
		t.Fatalf("service = %+v, want name/slug %q", res[0].Service, "postgres")
	}

	svcs, err := st.ListServices(proj.ID)
	if err != nil || len(svcs) != 1 {
		t.Fatalf("services = %v, %v; want exactly one", svcs, err)
	}
	if svcs[0].Status != "running" {
		t.Fatalf("service status = %q, want running", svcs[0].Status)
	}

	if len(rt.created) != 1 {
		t.Fatalf("created specs = %d, want 1", len(rt.created))
	}
	spec := rt.created[0]
	if spec.Name != "dm-svc-postgres" || spec.Image != Postgres.Image || spec.Network != "test-net" {
		t.Fatalf("spec = %+v", spec)
	}
	if len(spec.Binds) != 1 || spec.Binds[0] != "dm-svc-postgres-data:/var/lib/postgresql/data" {
		t.Fatalf("binds = %v", spec.Binds)
	}
	if len(spec.Env) != 3 {
		t.Fatalf("env = %v, want 3 entries", spec.Env)
	}
	if rt.execs < 1 {
		t.Fatal("readiness probe never ran")
	}

	// Credentials were generated and are decryptable.
	credsEnc, err := st.GetServiceCredentials(svcs[0].ID)
	if err != nil || len(credsEnc) != 3 {
		t.Fatalf("credentials = %v, %v; want 3", credsEnc, err)
	}
	creds := decryptCreds([32]byte{7}, credsEnc)
	if len(creds) != 3 || creds["password"] == "" {
		t.Fatalf("decrypted creds = %v", creds)
	}
}

func TestEnsureReusesRunningService(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	first := ensureOnce(t, p, proj.ID, "postgres")
	second := ensureOnce(t, p, proj.ID, "postgres")
	if len(second) != 1 || second[0].Action != ActionReused {
		t.Fatalf("second resolutions = %+v, want one reused", second)
	}
	if second[0].Service.ID != first[0].Service.ID {
		t.Fatalf("reused a different service: %q vs %q", second[0].Service.ID, first[0].Service.ID)
	}
	if len(rt.created) != 1 {
		t.Fatalf("created specs = %d, want still 1 (no re-create on reuse)", len(rt.created))
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 1 {
		t.Fatalf("service rows = %d, want 1 (no duplicates)", len(svcs))
	}
}

func TestEnsureStartsStoppedService(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	first := ensureOnce(t, p, proj.ID, "postgres")
	if err := st.UpdateServiceStatus(first[0].Service.ID, "stopped"); err != nil {
		t.Fatalf("stop service: %v", err)
	}

	second := ensureOnce(t, p, proj.ID, "postgres")
	if len(second) != 1 || second[0].Action != ActionStarted {
		t.Fatalf("second resolutions = %+v, want one started", second)
	}
	if second[0].Service.ID != first[0].Service.ID {
		t.Fatalf("started a different service: %q vs %q", second[0].Service.ID, first[0].Service.ID)
	}
	if len(rt.created) != 2 {
		t.Fatalf("created specs = %d, want 2 (re-created on start)", len(rt.created))
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 1 || svcs[0].Status != "running" {
		t.Fatalf("services = %+v, want one running", svcs)
	}
}

func TestEnsureNeverTouchesUndeclaredServices(t *testing.T) {
	p, st, _ := newTestProvisioner(t)
	proj := testProject(t, st)

	// A human-created redis service exists; a deploy declares postgres.
	if _, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "redis", Name: "Cache", Slug: "cache",
		Image: Redis.Image, Status: "stopped", VolumeName: VolumeName("cache"), Port: Redis.Port,
	}); err != nil {
		t.Fatalf("create redis: %v", err)
	}

	ensureOnce(t, p, proj.ID, "postgres")

	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 2 {
		t.Fatalf("services = %d, want 2 (redis untouched + postgres)", len(svcs))
	}
	byType := map[string]store.Service{}
	for _, svc := range svcs {
		byType[svc.Type] = svc
	}
	if byType["redis"].Status != "stopped" {
		t.Fatalf("redis status = %q, want untouched (stopped)", byType["redis"].Status)
	}
	if byType["postgres"].Status != "running" {
		t.Fatalf("postgres status = %q, want running", byType["postgres"].Status)
	}
}

func TestEnsurePrefersRunningServiceOfType(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	// Two postgres services: the newest is stopped, the older is running.
	oldSvc, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "postgres", Name: "Main DB", Slug: "main-db",
		Image: Postgres.Image, Status: "running", VolumeName: VolumeName("main-db"), Port: Postgres.Port,
	})
	if err != nil {
		t.Fatalf("create old postgres: %v", err)
	}
	if _, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "postgres", Name: "Analytics", Slug: "analytics",
		Image: Postgres.Image, Status: "stopped", VolumeName: VolumeName("analytics"), Port: Postgres.Port,
	}); err != nil {
		t.Fatalf("create new postgres: %v", err)
	}

	res := ensureOnce(t, p, proj.ID, "postgres")
	if len(res) != 1 || res[0].Action != ActionReused {
		t.Fatalf("resolutions = %+v, want one reused", res)
	}
	if res[0].Service.ID != oldSvc.ID {
		t.Fatalf("reused %q, want the running service %q", res[0].Service.ID, oldSvc.ID)
	}
	if len(rt.created) != 0 {
		t.Fatalf("created specs = %d, want 0", len(rt.created))
	}
}

func TestEnsureUnknownTypeFails(t *testing.T) {
	p, st, _ := newTestProvisioner(t)
	proj := testProject(t, st)

	_, err := p.Ensure(context.Background(), proj.ID, []string{"mongo"})
	if err == nil {
		t.Fatal("Ensure(mongo) succeeded, want error")
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 0 {
		t.Fatalf("services = %d, want 0 (failure leaves nothing behind)", len(svcs))
	}
}

func TestEnsureReadinessFailureMarksFailed(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)
	rt.failExec = true

	_, err := p.Ensure(context.Background(), proj.ID, []string{"postgres"})
	if err == nil {
		t.Fatal("Ensure succeeded, want readiness error")
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 1 || svcs[0].Status != "failed" {
		t.Fatalf("services = %+v, want one failed", svcs)
	}
}
