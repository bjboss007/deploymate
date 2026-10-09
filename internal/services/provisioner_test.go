package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
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
	// What Inspect and Logs report (zero values: container not found, no logs).
	info    *runtime.Info
	logText string
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
func (f *fakeRuntime) Rename(ctx context.Context, oldName, newName string) error { return nil }
func (f *fakeRuntime) Inspect(ctx context.Context, name string) (runtime.Info, error) {
	if f.info != nil {
		return *f.info, nil
	}
	return runtime.Info{}, runtime.ErrContainerNotFound
}
func (f *fakeRuntime) Logs(ctx context.Context, name string, follow bool, tail int) (io.ReadCloser, error) {
	if f.logText == "" {
		return nil, nil
	}
	// docker multiplexes stdout/stderr: an 8-byte header (stream, 0,0,0, big-endian length) per frame.
	n := len(f.logText)
	hdr := []byte{1, 0, 0, 0, byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	return io.NopCloser(bytes.NewReader(append(hdr, f.logText...))), nil
}
func (f *fakeRuntime) Exec(ctx context.Context, name string, cmd []string) (string, error) {
	return f.execEnv(ctx, name, cmd, nil)
}
func (f *fakeRuntime) ExecEnv(ctx context.Context, name string, cmd, env []string) (string, error) {
	return f.execEnv(ctx, name, cmd, env)
}
func (f *fakeRuntime) execEnv(_ context.Context, _ string, _ []string, _ []string) (string, error) {
	f.execs++
	if f.failExec {
		return "", errors.New("probe failed")
	}
	return "ok", nil
}
func (f *fakeRuntime) WriteFile(context.Context, string, string, []byte) error { return nil }
func (f *fakeRuntime) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("no files in the provisioner fake")
}
func (f *fakeRuntime) Stats(ctx context.Context, name string) (runtime.Stats, error) {
	return runtime.Stats{}, nil
}
func (f *fakeRuntime) StorageUsed(ctx context.Context) (uint64, error) { return 0, nil }
func (f *fakeRuntime) DiskUsage(context.Context) (runtime.DiskUsage, error) {
	return runtime.DiskUsage{}, nil
}
func (f *fakeRuntime) ImageSize(context.Context, string) (uint64, error) { return 0, nil }
func (f *fakeRuntime) Close() error                                      { return nil }

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

// ensureOnce calls Ensure in production with the given bare types.
func ensureOnce(t *testing.T, p *Provisioner, projectID string, types ...string) []Resolution {
	t.Helper()
	decls := make([]ServiceDecl, 0, len(types))
	for _, typ := range types {
		decls = append(decls, ServiceDecl{Type: typ})
	}
	res, err := p.Ensure(context.Background(), projectID, store.EnvProduction, decls)
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
	if res[0].Service.Environment != store.EnvProduction {
		t.Fatalf("service environment = %q, want production", res[0].Service.Environment)
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
	if spec.Name != "dm-svc-postgres" || spec.Network != "test-net" {
		t.Fatalf("spec = %+v", spec)
	}
	// No version declared → the latest image of the type.
	if spec.Image != "postgres:latest" {
		t.Fatalf("spec image = %q, want postgres:latest (no version → latest)", spec.Image)
	}
	if len(spec.Binds) != 1 || spec.Binds[0] != "dm-svc-postgres-data:/var/lib/postgresql/data" {
		t.Fatalf("binds = %v", spec.Binds)
	}
	// PGDATA pins the data directory to the mounted volume: Postgres 18 would otherwise
	// look in /var/lib/postgresql/18/docker and refuse to start beside a volume mounted
	// at the old path (found live 2026-10-09 with a bare "postgres" in deploymate.yml).
	if len(spec.Env) != 4 || !containsStr(spec.Env, "PGDATA=/var/lib/postgresql/data") {
		t.Fatalf("env = %v, want 4 entries including PGDATA=/var/lib/postgresql/data", spec.Env)
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

func TestEnsurePinnedImage(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	res, err := p.Ensure(context.Background(), proj.ID, store.EnvProduction, []ServiceDecl{{Type: "postgres", Pin: "17"}})
	if err != nil {
		t.Fatalf("Ensure(pinned) error = %v", err)
	}
	if len(res) != 1 || res[0].Service.Image != "postgres:17" {
		t.Fatalf("service = %+v, want image postgres:17", res[0].Service)
	}
	if len(rt.created) != 1 || rt.created[0].Image != "postgres:17" {
		t.Fatalf("created specs = %+v, want image postgres:17", rt.created)
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

// A manifest-created service the manifest stops declaring is flagged
// orphaned and reported once — but never deleted, and re-declaring clears it.
func TestEnsureFlagsAndClearsOrphan(t *testing.T) {
	p, st, _ := newTestProvisioner(t)
	proj := testProject(t, st)

	// First deploy declares postgres + redis: both are manifest-created.
	ensureOnce(t, p, proj.ID, "postgres", "redis")
	redis, err := st.GetServiceBySlug("redis")
	if err != nil {
		t.Fatalf("get redis: %v", err)
	}
	if redis.Origin != store.OriginManifest {
		t.Fatalf("redis origin = %q, want manifest", redis.Origin)
	}

	// Next deploy drops redis → orphan candidate, reported once, not deleted.
	res := ensureOnce(t, p, proj.ID, "postgres")
	var orphans []Resolution
	for _, r := range res {
		if r.Action == ActionOrphaned {
			orphans = append(orphans, r)
		}
	}
	if len(orphans) != 1 || orphans[0].Type != "redis" {
		t.Fatalf("orphan resolutions = %+v, want one for redis", orphans)
	}
	redis, _ = st.GetServiceBySlug("redis")
	if !redis.Orphaned {
		t.Fatal("redis not flagged orphaned")
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 2 {
		t.Fatalf("services = %d, want 2 (orphan is flagged, never deleted)", len(svcs))
	}

	// A redeploy that still omits redis does not re-report the known orphan.
	res = ensureOnce(t, p, proj.ID, "postgres")
	for _, r := range res {
		if r.Action == ActionOrphaned {
			t.Fatalf("known orphan re-reported: %+v", r)
		}
	}

	// Re-declaring redis clears the flag.
	ensureOnce(t, p, proj.ID, "postgres", "redis")
	redis, _ = st.GetServiceBySlug("redis")
	if redis.Orphaned {
		t.Fatal("redis still orphaned after being re-declared")
	}
}

// A manually-created service is never flagged when the manifest drops nothing
// it owns — origin 'manual' is invisible to orphan reconciliation.
func TestEnsureNeverFlagsManualService(t *testing.T) {
	p, st, _ := newTestProvisioner(t)
	proj := testProject(t, st)

	if _, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "redis", Name: "Cache", Slug: "cache",
		Image: Redis.Image, Status: "stopped", VolumeName: VolumeName("cache"), Port: Redis.Port,
	}); err != nil {
		t.Fatalf("create manual redis: %v", err)
	}

	res := ensureOnce(t, p, proj.ID, "postgres")
	for _, r := range res {
		if r.Action == ActionOrphaned {
			t.Fatalf("manual service flagged orphaned: %+v", r)
		}
	}
	cache, _ := st.GetServiceBySlug("cache")
	if cache.Orphaned {
		t.Fatal("manual redis flagged orphaned, want untouched")
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

	_, err := p.Ensure(context.Background(), proj.ID, store.EnvProduction, []ServiceDecl{{Type: "mongo"}})
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

	_, err := p.Ensure(context.Background(), proj.ID, store.EnvProduction, []ServiceDecl{{Type: "postgres"}})
	if err == nil {
		t.Fatal("Ensure succeeded, want readiness error")
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 1 || svcs[0].Status != "failed" {
		t.Fatalf("services = %+v, want one failed", svcs)
	}
}

func TestEnsureCreatesStagingService(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	res, err := p.Ensure(context.Background(), proj.ID, store.EnvStaging, []ServiceDecl{{Type: "postgres"}})
	if err != nil {
		t.Fatalf("Ensure(staging) error = %v", err)
	}
	if len(res) != 1 || res[0].Action != ActionProvisioned {
		t.Fatalf("resolutions = %+v, want one provisioned", res)
	}
	svc := res[0].Service
	if svc.Name != "Staging PostgreSQL" || svc.Slug != "staging-postgres" || svc.Environment != store.EnvStaging {
		t.Fatalf("staging service = %+v, want name %q slug %q env %q", svc, "Staging PostgreSQL", "staging-postgres", store.EnvStaging)
	}
	if len(rt.created) != 1 {
		t.Fatalf("created specs = %d, want 1", len(rt.created))
	}
	spec := rt.created[0]
	if spec.Name != "dm-svc-staging-postgres" {
		t.Fatalf("container name = %q, want dm-svc-staging-postgres", spec.Name)
	}
	if len(spec.Binds) != 1 || spec.Binds[0] != "dm-svc-staging-postgres-data:/var/lib/postgresql/data" {
		t.Fatalf("binds = %v, want staging volume", spec.Binds)
	}

	// Production still gets its own service — no slug collision.
	res2 := ensureOnce(t, p, proj.ID, "postgres")
	if len(res2) != 1 || res2[0].Service.Slug != "postgres" {
		t.Fatalf("production resolutions = %+v", res2)
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 2 {
		t.Fatalf("service rows = %d, want 2 (one per environment)", len(svcs))
	}
}

func TestEnsureCreatesDevService(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	res, err := p.Ensure(context.Background(), proj.ID, store.EnvDev, []ServiceDecl{{Type: "postgres"}})
	if err != nil {
		t.Fatalf("Ensure(dev) error = %v", err)
	}
	if len(res) != 1 || res[0].Action != ActionProvisioned {
		t.Fatalf("resolutions = %+v, want one provisioned", res)
	}
	svc := res[0].Service
	if svc.Name != "Dev PostgreSQL" || svc.Slug != "dev-postgres" || svc.Environment != store.EnvDev {
		t.Fatalf("dev service = %+v, want name %q slug %q env %q", svc, "Dev PostgreSQL", "dev-postgres", store.EnvDev)
	}
	if len(rt.created) != 1 || rt.created[0].Name != "dm-svc-dev-postgres" {
		t.Fatalf("created specs = %+v, want one dm-svc-dev-postgres", rt.created)
	}
}

func TestEnsureSeparatesEnvironments(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	proj := testProject(t, st)

	// Production postgres exists and runs.
	prodRes := ensureOnce(t, p, proj.ID, "postgres")
	// A staging deploy must not reuse or touch it.
	stagingRes, err := p.Ensure(context.Background(), proj.ID, store.EnvStaging, []ServiceDecl{{Type: "postgres"}})
	if err != nil {
		t.Fatalf("Ensure(staging) error = %v", err)
	}
	if stagingRes[0].Action != ActionProvisioned {
		t.Fatalf("staging action = %q, want provisioned (production must not be reused)", stagingRes[0].Action)
	}
	if stagingRes[0].Service.ID == prodRes[0].Service.ID {
		t.Fatal("staging reused the production service")
	}
	svcs, _ := st.ListServices(proj.ID)
	if len(svcs) != 2 {
		t.Fatalf("service rows = %d, want 2 (one per environment)", len(svcs))
	}
	// The production service was never re-created or restarted.
	if len(rt.created) != 2 {
		t.Fatalf("created specs = %d, want 2 (one per environment)", len(rt.created))
	}
	if rt.created[0].Name != "dm-svc-postgres" || rt.created[1].Name != "dm-svc-staging-postgres" {
		t.Fatalf("created names = %v", []string{rt.created[0].Name, rt.created[1].Name})
	}
}

func TestEnsureStagingReusesOnlyStaging(t *testing.T) {
	p, st, _ := newTestProvisioner(t)
	proj := testProject(t, st)

	prodRes := ensureOnce(t, p, proj.ID, "postgres")
	stagingFirst, err := p.Ensure(context.Background(), proj.ID, store.EnvStaging, []ServiceDecl{{Type: "postgres"}})
	if err != nil {
		t.Fatalf("first staging Ensure error = %v", err)
	}

	stagingSecond, err := p.Ensure(context.Background(), proj.ID, store.EnvStaging, []ServiceDecl{{Type: "postgres"}})
	if err != nil {
		t.Fatalf("second staging Ensure error = %v", err)
	}
	if len(stagingSecond) != 1 || stagingSecond[0].Action != ActionReused {
		t.Fatalf("staging resolutions = %+v, want one reused", stagingSecond)
	}
	if stagingSecond[0].Service.ID != stagingFirst[0].Service.ID {
		t.Fatalf("staging reused %q, want its own %q", stagingSecond[0].Service.ID, stagingFirst[0].Service.ID)
	}
	if stagingSecond[0].Service.ID == prodRes[0].Service.ID {
		t.Fatal("staging reuse matched the production service")
	}
}

// TestEnsureDoesNotTakeAnotherProjectsService: container and volume names are
// global (dm-svc-{slug}), so a second project's manifest-created dev-postgres
// must get its OWN slug instead of re-provisioning — stop, remove, create
// over the same name — the first project's container.
func TestEnsureDoesNotTakeAnotherProjectsService(t *testing.T) {
	p, st, rt := newTestProvisioner(t)
	first := testProject(t, st)
	owner, err := st.GetUserByEmail("owner@test.dev")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Acme Starter", Slug: "acme-starter"})
	if err != nil {
		t.Fatal(err)
	}

	r1 := ensureOnceEnv(t, p, first.ID, store.EnvDev, "redis")
	if r1[0].Service.Slug != "dev-redis" {
		t.Fatalf("first project's slug = %q", r1[0].Service.Slug)
	}
	removedBefore := len(rt.removed)
	r2 := ensureOnceEnv(t, p, second.ID, store.EnvDev, "redis")
	for _, n := range rt.removed[removedBefore:] {
		if n == "dm-svc-dev-redis" {
			t.Fatal("the second project's deploy removed the first project's container")
		}
	}
	svc := r2[0].Service
	if svc.Slug != "acme-starter-dev-redis" || svc.VolumeName != "dm-svc-acme-starter-dev-redis-data" {
		t.Fatalf("second project's service = slug %q volume %q, want its own project-prefixed names", svc.Slug, svc.VolumeName)
	}
	names := map[string]bool{}
	for _, s := range rt.created {
		names[s.Name] = true
	}
	if !names["dm-svc-dev-redis"] || !names["dm-svc-acme-starter-dev-redis"] || len(rt.created) != 2 {
		t.Fatalf("created containers = %v, want two distinct names", names)
	}
	// Re-running the second project's deploy converges on its own service.
	r3 := ensureOnceEnv(t, p, second.ID, store.EnvDev, "redis")
	if r3[0].Service.ID != svc.ID {
		t.Errorf("second Ensure created another service (%q), want to reuse %q", r3[0].Service.Slug, svc.Slug)
	}
}

func ensureOnceEnv(t *testing.T, p *Provisioner, projectID, env string, types ...string) []Resolution {
	t.Helper()
	decls := make([]ServiceDecl, 0, len(types))
	for _, typ := range types {
		decls = append(decls, ServiceDecl{Type: typ})
	}
	res, err := p.Ensure(context.Background(), projectID, env, decls)
	if err != nil {
		t.Fatalf("Ensure(%s %v) error = %v", env, types, err)
	}
	return res
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// A service container that has already exited fails the deploy at once, and the
// error carries what it printed (the Postgres 18 refusal, a bad password setting…).
func TestWaitReadyFailsFastAndQuotesTheLogsOfAnExitedContainer(t *testing.T) {
	p, _, rt := newTestProvisioner(t)
	rt.failExec = true
	rt.info = &runtime.Info{Running: false, State: "exited", ExitCode: 1}
	rt.logText = "Error: in 18+, these Docker images are configured to store database data in a\nformat which is compatible with pg_ctlcluster\n"
	readinessAttempts, readinessInterval = 30, time.Hour // a wait would hang the test
	err := p.waitReady(context.Background(), "dm-svc-x", Postgres, map[string]string{"user": "dm", "db": "app"})
	if err == nil {
		t.Fatal("an exited container cannot become ready")
	}
	for _, want := range []string{"service did not become ready", "exited with code 1", "in 18+, these Docker images"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "within 60s") {
		t.Error("an exited container must not wait out the minute")
	}
	if rt.execs != 1 {
		t.Errorf("probed %d times, want 1", rt.execs)
	}
}

// A container that stays up but never answers still times out, now saying what it printed.
func TestWaitReadyTimeoutQuotesTheLogs(t *testing.T) {
	p, _, rt := newTestProvisioner(t)
	rt.failExec = true
	rt.info = &runtime.Info{Running: true, State: "running"}
	rt.logText = "starting up, still recovering\n"
	err := p.waitReady(context.Background(), "dm-svc-x", Postgres, map[string]string{"user": "dm", "db": "app"})
	if err == nil || !strings.Contains(err.Error(), "within 60s") || !strings.Contains(err.Error(), "still recovering") {
		t.Errorf("timeout error = %v", err)
	}
}
