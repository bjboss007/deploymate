package backup

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/config"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeRT is a scriptable runtime: an in-container files map, a running flag,
// and a record of every exec (cmd + env). execFn, when set, lets a test
// script side effects (e.g. pg_dump dropping a dump file into the map).
type fakeRT struct {
	mu         sync.Mutex
	running    bool
	inspectErr error
	files      map[string][]byte
	calls      []execCall
	execFn     func(c execCall) (string, error)
	readErr    error // ReadFile failure override
	writeErr   error // WriteFile failure override
}

type execCall struct {
	cmd []string
	env []string
}

func (f *fakeRT) execCalls() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]execCall(nil), f.calls...)
}

func (f *fakeRT) EnsureNetwork(context.Context, string) error          { return nil }
func (f *fakeRT) PullImage(context.Context, string) error              { return nil }
func (f *fakeRT) HasImage(context.Context, string) (bool, error)       { return true, nil }
func (f *fakeRT) RemoveImage(context.Context, string) (bool, error)    { return true, nil }
func (f *fakeRT) Create(context.Context, runtime.Spec) (string, error) { return "cid", nil }
func (f *fakeRT) Start(context.Context, string) error                  { return nil }
func (f *fakeRT) Stop(context.Context, string, int) error              { return nil }
func (f *fakeRT) Remove(context.Context, string) error                 { return nil }
func (f *fakeRT) Rename(context.Context, string, string) error         { return nil }
func (f *fakeRT) Inspect(context.Context, string) (runtime.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return runtime.Info{Running: f.running}, f.inspectErr
}
func (f *fakeRT) Logs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeRT) Exec(ctx context.Context, name string, cmd []string) (string, error) {
	return f.ExecEnv(ctx, name, cmd, nil)
}
func (f *fakeRT) ExecEnv(_ context.Context, _ string, cmd, env []string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, execCall{cmd: append([]string(nil), cmd...), env: append([]string(nil), env...)})
	fn := f.execFn
	f.mu.Unlock()
	if fn != nil {
		return fn(execCall{cmd: cmd, env: env})
	}
	return "", nil
}
func (f *fakeRT) WriteFile(_ context.Context, _ string, containerPath string, content []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.files[containerPath] = append([]byte(nil), content...)
	return nil
}
func (f *fakeRT) ReadFile(_ context.Context, _ string, containerPath string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	b, ok := f.files[containerPath]
	if !ok {
		return nil, runtime.ErrContainerNotFound
	}
	return append([]byte(nil), b...), nil
}
func (f *fakeRT) Stats(context.Context, string) (runtime.Stats, error) { return runtime.Stats{}, nil }
func (f *fakeRT) StorageUsed(context.Context) (uint64, error)          { return 0, nil }
func (f *fakeRT) DiskUsage(context.Context) (runtime.DiskUsage, error) {
	return runtime.DiskUsage{}, nil
}
func (f *fakeRT) ImageSize(context.Context, string) (uint64, error) { return 0, nil }
func (f *fakeRT) Close() error                                      { return nil }

// newTestManager seeds a postgres service with encrypted credentials and a
// Manager over a temp local destination named "default".
func newTestManager(t *testing.T) (*Manager, *store.Store, store.Service, *fakeRT, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var encKey [32]byte
	encKey[0] = 1
	owner, err := st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "T", Slug: "t"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	svc, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "postgres", Name: "PG", Slug: "pg",
		Image: "postgres:16-alpine", Status: "running", VolumeName: "dm-svc-pg-data", Port: 5432,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	creds := map[string]string{}
	for k, v := range map[string]string{"user": "dm", "password": "pw-secret", "db": "app"} {
		enc, err := crypto.Encrypt(encKey, v)
		if err != nil {
			t.Fatal(err)
		}
		creds[k] = enc
	}
	if err := st.SetServiceCredentials(svc.ID, creds); err != nil {
		t.Fatalf("set creds: %v", err)
	}

	destDir := t.TempDir()
	dests, err := NewDestinations(map[string]config.BackupDestination{
		"default": {ID: "default", Type: "local", Dir: destDir},
	})
	if err != nil {
		t.Fatalf("destinations: %v", err)
	}
	rt := &fakeRT{running: true, files: map[string][]byte{}}
	mgr := NewManager(st, rt, encKey, alerts.New(st, encKey), dests)
	return mgr, st, svc, rt, destDir
}

// enable flips a service on via SaveConfig (exercising key generation) and
// returns the stored config.
func enable(t *testing.T, mgr *Manager, svc store.Service, mut ...func(*store.BackupConfig)) store.BackupConfig {
	t.Helper()
	cfg := store.BackupConfig{Enabled: true, Schedule: "0 2 * * *", Keep: 14, Destination: "default"}
	for _, m := range mut {
		m(&cfg)
	}
	if err := mgr.SaveConfig(svc, cfg); err != nil {
		t.Fatalf("enable: %v", err)
	}
	got, err := mgr.st.GetBackupConfig(svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// hasEvent reports whether the events table holds a row of the given kind.
func hasEvent(st *store.Store, kind string) bool {
	evs, err := st.ListEvents("", 500)
	if err != nil {
		return false
	}
	for _, e := range evs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func lastRunAt(t *testing.T, st *store.Store, svc store.Service) string {
	t.Helper()
	cfg, err := st.GetBackupConfig(svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.LastRunAt
}
