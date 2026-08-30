package httpserver

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeRuntime records calls for the start/heal path; everything else is a
// no-op success.
type fakeRuntime struct {
	started   int
	created   int
	lastSpec  runtime.Spec
	info      runtime.Info
	inspectErr error
}

func (f *fakeRuntime) EnsureNetwork(context.Context, string) error          { return nil }
func (f *fakeRuntime) PullImage(context.Context, string) error              { return nil }
func (f *fakeRuntime) HasImage(context.Context, string) (bool, error)       { return true, nil }
func (f *fakeRuntime) RemoveImage(context.Context, string) (bool, error)    { return true, nil }
func (f *fakeRuntime) Create(_ context.Context, spec runtime.Spec) (string, error) {
	f.created++
	f.lastSpec = spec
	return "cid", nil
}
func (f *fakeRuntime) Start(context.Context, string) error     { f.started++; return nil }
func (f *fakeRuntime) Stop(context.Context, string, int) error { return nil }
func (f *fakeRuntime) Remove(context.Context, string) error    { return nil }
func (f *fakeRuntime) Inspect(context.Context, string) (runtime.Info, error) {
	return f.info, f.inspectErr
}
func (f *fakeRuntime) Logs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeRuntime) Exec(context.Context, string, []string) (string, error) { return "", nil }
func (f *fakeRuntime) Stats(context.Context, string) (runtime.Stats, error)   { return runtime.Stats{}, nil }
func (f *fakeRuntime) StorageUsed(context.Context) (uint64, error)            { return 0, nil }
func (f *fakeRuntime) Close() error                                           { return nil }

// TestAppEnvFiltersByEnvironment proves URL injection is environment-scoped:
// a production app sees only production services and a staging app only
// staging ones.
func TestAppEnvFiltersByEnvironment(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	encKey := [32]byte{7}
	s := &Server{store: st, encKey: encKey}

	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	prodApp, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Web", Slug: "web", Environment: store.EnvProduction})
	if err != nil {
		t.Fatalf("create prod app: %v", err)
	}
	stagingApp, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Web", Slug: "web-staging", Environment: store.EnvStaging})
	if err != nil {
		t.Fatalf("create staging app: %v", err)
	}

	mkService := func(slug, env string) {
		t.Helper()
		tpl, _ := services.ForType("postgres")
		svc, err := st.CreateService(store.Service{
			ProjectID: proj.ID, Type: "postgres", Name: slug, Slug: slug,
			Image: tpl.Image, Status: "running", VolumeName: services.VolumeName(slug), Port: tpl.Port,
			Environment: env,
		})
		if err != nil {
			t.Fatalf("create service %s: %v", slug, err)
		}
		enc := map[string]string{}
		for k, v := range map[string]string{"user": "dm", "password": "pw", "db": "app"} {
			enc[k], err = crypto.Encrypt(encKey, v)
			if err != nil {
				t.Fatalf("encrypt cred: %v", err)
			}
		}
		if err := st.SetServiceCredentials(svc.ID, enc); err != nil {
			t.Fatalf("set creds: %v", err)
		}
	}
	mkService("postgres", store.EnvProduction)
	mkService("staging-postgres", store.EnvStaging)

	prodEnv := strings.Join(s.AppEnv(prodApp), "\n")
	if !strings.Contains(prodEnv, "DATABASE_URL=postgres://dm:pw@dm-svc-postgres:5432/app") {
		t.Fatalf("production app env missing production URL:\n%s", prodEnv)
	}
	if strings.Contains(prodEnv, "dm-svc-staging-postgres") {
		t.Fatalf("production app env leaked a staging URL:\n%s", prodEnv)
	}

	stagingEnv := strings.Join(s.AppEnv(stagingApp), "\n")
	if !strings.Contains(stagingEnv, "DATABASE_URL=postgres://dm:pw@dm-svc-staging-postgres:5432/app") {
		t.Fatalf("staging app env missing staging URL:\n%s", stagingEnv)
	}
	if strings.Contains(stagingEnv, "dm-svc-postgres:5432") {
		t.Fatalf("staging app env leaked a production URL:\n%s", stagingEnv)
	}
}

// TestDeployPort proves the deploy form's empty port field keeps the app's
// stored port instead of clobbering it to 0 (which drops the preview
// binding and strands the app as permanently "unhealthy").
func TestDeployPort(t *testing.T) {
	cases := []struct {
		form   string
		stored int
		want   int
	}{
		{"", 8080, 8080},     // empty form keeps the stored port
		{"   ", 3000, 3000},  // whitespace is an empty field
		{"5000", 8080, 5000}, // explicit value wins
		{"abc", 8080, 8080},  // garbage falls back, never 0
		{"0", 8080, 0},       // an explicit 0 is respected (portless app)
	}
	for _, c := range cases {
		if got := deployPort(c.form, c.stored); got != c.want {
			t.Errorf("deployPort(%q, %d) = %d, want %d", c.form, c.stored, got, c.want)
		}
	}
}

// TestHealthReasonFor proves the unhealthy badge no longer claims
// "crash-looping" for a running container with zero restarts.
func TestHealthReasonFor(t *testing.T) {
	crash := healthReasonFor(runtime.Info{Running: true, Restarts: 4})
	if !strings.Contains(crash, "crash-looping") || !strings.Contains(crash, "4 restarts") {
		t.Errorf("crash-looping reason wrong: %q", crash)
	}
	up := healthReasonFor(runtime.Info{Running: true, Restarts: 0})
	if strings.Contains(up, "crash-looping") {
		t.Errorf("0-restart reason must not claim a crash loop: %q", up)
	}
	if !strings.Contains(up, "preview port") {
		t.Errorf("0-restart reason should point at the probe: %q", up)
	}
	down := healthReasonFor(runtime.Info{Running: false})
	if !strings.Contains(down, "not running") {
		t.Errorf("stopped reason wrong: %q", down)
	}
}

// TestStartAppHealsMissingBinding proves start recreates a container that
// was created without its preview port binding, and leaves containers that
// are already reachable (or git-built) alone.
func TestStartAppHealsMissingBinding(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	mkApp := func(slug string, port int, gitSourceID string) store.App {
		t.Helper()
		app, err := st.CreateApp(store.App{
			ProjectID: proj.ID, Name: slug, Slug: slug,
			Status: "running", Port: port, Image: "example.com/app:1",
			GitSourceID: gitSourceID,
		})
		if err != nil {
			t.Fatalf("create app %s: %v", slug, err)
		}
		return app
	}

	t.Run("unbound image app is recreated with the preview binding", func(t *testing.T) {
		app := mkApp("img-app", 8080, "")
		fake := &fakeRuntime{info: runtime.Info{Running: true, Restarts: 0, PublishedPorts: nil}}
		s := &Server{store: st, rt: fake}

		if err := s.startApp(context.Background(), app); err != nil {
			t.Fatalf("startApp: %v", err)
		}
		if fake.created != 1 {
			t.Fatalf("expected one recreate, got %d", fake.created)
		}
		if fake.lastSpec.Port != 8080 || fake.lastSpec.HostPort != runtime.PreviewPort("img-app") {
			t.Errorf("recreated spec missing preview binding: port=%d hostport=%d", fake.lastSpec.Port, fake.lastSpec.HostPort)
		}
	})

	t.Run("bound container is left alone", func(t *testing.T) {
		app := mkApp("bound-app", 8080, "")
		fake := &fakeRuntime{info: runtime.Info{Running: true, Restarts: 0, PublishedPorts: []string{"8080/tcp"}}}
		s := &Server{store: st, rt: fake}

		if err := s.startApp(context.Background(), app); err != nil {
			t.Fatalf("startApp: %v", err)
		}
		if fake.created != 0 {
			t.Errorf("bound container must not be recreated, created=%d", fake.created)
		}
	})

	t.Run("git-source container is never recreated from app.Image", func(t *testing.T) {
		gs, err := st.CreateGitSource(store.GitSource{
			Provider: "github", RepoURL: "https://github.com/x/y",
			CloneMethod: "ssh", DefaultBranch: "main",
		})
		if err != nil {
			t.Fatalf("create git source: %v", err)
		}
		app := mkApp("git-app", 8080, gs.ID)
		fake := &fakeRuntime{info: runtime.Info{Running: true, Restarts: 0, PublishedPorts: nil}}
		s := &Server{store: st, rt: fake}

		if err := s.startApp(context.Background(), app); err != nil {
			t.Fatalf("startApp: %v", err)
		}
		if fake.created != 0 {
			t.Errorf("git-source container must not be recreated, created=%d", fake.created)
		}
	})
}
