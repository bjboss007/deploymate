package noderoute

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/store"
)

type loc struct{}

func (loc) HostFor(name string) string {
	if strings.HasPrefix(name, "dm-onnode") {
		return "100.64.0.5"
	}
	return "127.0.0.1"
}

func setup(t *testing.T) (*Reconciler, *store.Store, store.App, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	owner, _ := st.CreateUser(store.User{Email: "o@x.com", PasswordHash: "x", Role: "owner"})
	n, _ := st.CreateNode(store.Node{Name: "friend", Endpoint: "ssh://u@100.64.0.5", State: store.NodeReady})
	pin, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "Pinned", Slug: "pinned"})
	_ = st.SetProjectNode(pin.ID, n.ID)
	app, err := st.CreateApp(store.App{ProjectID: pin.ID, Name: "onnode", Slug: "onnode", Port: 8080, Status: "running", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return &Reconciler{St: st, Locator: loc{}, Dir: dir, Resolver: "letsencrypt"}, st, app, dir
}

func TestRenderRoutesDomainsToTheNodeAndRefusesBadInput(t *testing.T) {
	body, err := Render(Route{Slug: "onnode", Domains: []string{"a.example.com", "b.example.com"}, Servers: []string{"http://100.64.0.5:20123", "http://100.64.0.5:20124"}, HealthPath: "/health", Resolver: "letsencrypt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Host(`a.example.com`, `b.example.com`)", "url: http://100.64.0.5:20123", "url: http://100.64.0.5:20124", "certResolver: letsencrypt", "path: /health", "entryPoints:"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	for _, bad := range []Route{
		{Slug: "x", Domains: []string{"a.com`) || Host(`evil.com"}, Servers: []string{"http://h:1"}},
		{Slug: "x", Domains: []string{"UPPER.example.com"}, Servers: []string{"http://h:1"}},
		{Slug: "x", Servers: []string{"http://h:1"}},
		{Slug: "x", Domains: []string{"a.example.com"}},
	} {
		if _, err := Render(bad); err == nil {
			t.Errorf("%+v must be refused: the domain ends up inside a Traefik rule", bad)
		}
	}
	tunnel, _ := Render(Route{Slug: "x", Domains: []string{"a.example.com"}, Servers: []string{"http://h:1"}})
	if strings.Contains(string(tunnel), "certResolver") {
		t.Error("with no resolver (tunnel mode) no certificate must be requested")
	}
}

func TestReconcileWritesChangesAndRemovesRoutesAndNeverTouchesOtherFiles(t *testing.T) {
	r, st, app, dir := setup(t)
	if err := os.WriteFile(filepath.Join(dir, "dashboard.yml"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node-app-onnode.yml")

	if err := r.Once(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("an app without a domain has nothing to route")
	}

	_, _ = st.CreateDomain(store.Domain{AppID: app.ID, Hostname: "app.example.com"})
	_, _ = st.CreateDomain(store.Domain{AppID: app.ID, Hostname: "Bad Host!"})
	if err := r.Once(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("route not written: %v", err)
	}
	want := "url: http://100.64.0.5:" + strconv.Itoa(appspec.ResolvedPreviewPort(app))
	if !strings.Contains(string(raw), "Host(`app.example.com`)") || !strings.Contains(string(raw), want) || strings.Contains(string(raw), "Bad Host") {
		t.Fatalf("want %s in:\n%s", want, raw)
	}
	before, _ := os.Stat(path)
	_ = r.Once()
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged route must not be rewritten (Traefik reloads on every change)")
	}

	_ = st.UpdateAppStatus(app.ID, "stopped")
	_ = r.Once()
	if _, err := os.Stat(path); err == nil {
		t.Error("a stopped app must lose its route")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "dashboard.yml")); string(got) != "keep me" {
		t.Error("the reconciler must only touch its own files")
	}
}

func TestAppsOnThisServerGetNoNodeRoute(t *testing.T) {
	r, st, _, dir := setup(t)
	owner, _ := st.GetUserByEmail("o@x.com")
	plain, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "Plain", Slug: "plain"})
	local, _ := st.CreateApp(store.App{ProjectID: plain.ID, Name: "local", Slug: "local", Port: 8080, Status: "running", Environment: "production"})
	_, _ = st.CreateDomain(store.Domain{AppID: local.ID, Hostname: "local.example.com"})
	_ = r.Once()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("Traefik's Docker provider routes local apps: %v", entries)
	}
}
