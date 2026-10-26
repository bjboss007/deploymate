package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

type nodesEnv struct {
	*webhookEnv
	probeErr error
}

func newNodesEnv(t *testing.T) *nodesEnv {
	e := &nodesEnv{webhookEnv: newWebhookEnv(t)}
	e.s.dataDir = t.TempDir()
	e.s.SetNodeProbe(func(context.Context, string) (runtime.Capacity, error) {
		if e.probeErr != nil {
			return runtime.Capacity{}, e.probeErr
		}
		return runtime.Capacity{CPUs: 16, MemBytes: 64 << 30, OS: "Ubuntu 24.04", Arch: "x86_64", DockerVersion: "28.5.2", GPURuntime: true}, nil
	})
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	return e
}

func (e *nodesEnv) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		form.Set("csrf_token", "csrf")
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, body)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestNodesAddCheckDrainRemove(t *testing.T) {
	e := newNodesEnv(t)
	rec := e.do(http.MethodPost, "/nodes", url.Values{"name": {"friend-pc"}, "endpoint": {"ssh://habib@100.64.0.5"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "reachable") {
		t.Fatalf("add: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	list, _ := e.st.ListNodes()
	if len(list) != 1 || list[0].State != store.NodeReady || list[0].CPUs != 16 || !list[0].GPURuntime {
		t.Fatalf("%+v", list)
	}
	n := list[0]

	page := e.do(http.MethodGet, "/nodes", nil).Body.String()
	for _, want := range []string{"friend-pc", "ssh://habib@100.64.0.5", "16 CPUs", "64.0 GB", "Ubuntu 24.04", "Docker 28.5.2", "NVIDIA runtime", "A node sees everything that runs on it", `class="navlink on" href="/nodes"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}

	if rec := e.do(http.MethodPost, "/nodes/"+n.ID+"/drain", url.Values{}); !strings.Contains(rec.Header().Get("Location"), "draining") {
		t.Errorf("drain: %s", rec.Header().Get("Location"))
	}
	if got, _ := e.st.GetNode(n.ID); got.State != store.NodeDraining {
		t.Fatalf("state %s", got.State)
	}
	e.do(http.MethodPost, "/nodes/"+n.ID+"/drain", url.Values{})
	if got, _ := e.st.GetNode(n.ID); got.State != store.NodeReady {
		t.Fatalf("resume: %s", got.State)
	}

	e.probeErr = errors.New("ssh: Permission denied (publickey)")
	rec = e.do(http.MethodPost, "/nodes/"+n.ID+"/check", url.Values{})
	if !strings.Contains(rec.Header().Get("Location"), "not+reachable") {
		t.Errorf("check: %s", rec.Header().Get("Location"))
	}
	if got, _ := e.st.GetNode(n.ID); got.State != store.NodeOffline || !strings.Contains(got.LastError, "Permission denied") {
		t.Fatalf("%+v", got)
	}
	if page := e.do(http.MethodGet, "/nodes", nil).Body.String(); !strings.Contains(page, "Permission denied") {
		t.Error("the page should show why a node is offline")
	}

	e.do(http.MethodPost, "/nodes/"+n.ID+"/delete", url.Values{})
	if list, _ := e.st.ListNodes(); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestNodesRefuseBadInput(t *testing.T) {
	e := newNodesEnv(t)
	for name, form := range map[string]url.Values{
		"upper-case name": {"name": {"Friend"}, "endpoint": {"ssh://u@100.64.0.5"}},
		"option-looking":  {"name": {"x"}, "endpoint": {"ssh://-oProxyCommand=evil@host"}},
		"no user":         {"name": {"x"}, "endpoint": {"ssh://100.64.0.5"}},
		"wrong scheme":    {"name": {"x"}, "endpoint": {"http://100.64.0.5"}},
	} {
		if rec := e.do(http.MethodPost, "/nodes", form); !strings.Contains(rec.Header().Get("Location"), "flash=") {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if list, _ := e.st.ListNodes(); len(list) != 0 {
		t.Fatalf("a refused node must not be stored: %+v", list)
	}
	e.do(http.MethodPost, "/nodes", url.Values{"name": {"a"}, "endpoint": {"ssh://u@100.64.0.5"}})
	if rec := e.do(http.MethodPost, "/nodes", url.Values{"name": {"a"}, "endpoint": {"ssh://u@100.64.0.6"}}); !strings.Contains(rec.Header().Get("Location"), "already+exists") {
		t.Errorf("duplicate name: %s", rec.Header().Get("Location"))
	}
}

func TestNodeKeyIsCreatedOnceAndShown(t *testing.T) {
	e := newNodesEnv(t)
	if page := e.do(http.MethodGet, "/nodes", nil).Body.String(); !strings.Contains(page, "Create this server") {
		t.Fatal("the page should offer to create the key")
	}
	e.do(http.MethodPost, "/nodes/key", url.Values{})
	page := e.do(http.MethodGet, "/nodes", nil).Body.String()
	if !strings.Contains(page, "ssh-ed25519 ") || strings.Contains(page, "Create this server") {
		t.Fatal("the public key should be shown once it exists")
	}
	if strings.Contains(page, "PRIVATE KEY") {
		t.Fatal("the private key must never be shown")
	}
}

func TestNodesNeedASession(t *testing.T) {
	e := newNodesEnv(t)
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nodes", nil))
	if rec.Code == http.StatusOK {
		t.Fatal("the Nodes page must need a sign-in")
	}
}

func TestAProjectCanBePinnedToAReadyNodeOnlyBeforeAnythingIsDeployed(t *testing.T) {
	e := newNodesEnv(t)
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	n, _ := e.st.CreateNode(store.Node{Name: "friend", Endpoint: "ssh://u@100.64.0.5", State: store.NodeReady})

	page := e.do(http.MethodGet, "/projects/p", nil).Body.String()
	for _, want := range []string{"Where it runs", "This project runs on this server", `<option value="` + n.ID + `"`, "can read everything that runs on it"} {
		if !strings.Contains(page, want) {
			t.Errorf("project page missing %q", want)
		}
	}

	rec := e.do(http.MethodPost, "/projects/p/node", url.Values{"node": {n.ID}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "will+run+on+the+node") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	if id, _ := e.st.ProjectNodeID(proj.ID); id != n.ID {
		t.Fatalf("pinned to %q", id)
	}
	if page := e.do(http.MethodGet, "/projects/p", nil).Body.String(); !strings.Contains(page, "runs on the node <strong>friend</strong>") {
		t.Error("the page should say where the project runs")
	}

	// Back to this server is allowed while it is still empty.
	e.do(http.MethodPost, "/projects/p/node", url.Values{"node": {""}})
	if id, _ := e.st.ProjectNodeID(proj.ID); id != "" {
		t.Fatalf("unpinned to %q", id)
	}

	// An offline node cannot be chosen.
	_ = e.st.SetNodeProbe(n.ID, false, store.Node{}, "down")
	if rec := e.do(http.MethodPost, "/projects/p/node", url.Values{"node": {n.ID}}); !strings.Contains(rec.Header().Get("Location"), "offline") {
		t.Errorf("offline: %s", rec.Header().Get("Location"))
	}
	_ = e.st.SetNodeProbe(n.ID, true, store.Node{CPUs: 4}, "")

	// Once there is a service the project is locked where it is.
	_, _ = e.st.CreateService(store.Service{ProjectID: proj.ID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "stopped", Environment: "dev", Port: 6379})
	rec = e.do(http.MethodPost, "/projects/p/node", url.Values{"node": {n.ID}})
	if !strings.Contains(rec.Header().Get("Location"), "cannot+move") {
		t.Fatalf("a project with services must not move: %s", rec.Header().Get("Location"))
	}
	if id, _ := e.st.ProjectNodeID(proj.ID); id != "" {
		t.Fatal("it must stay where it was")
	}
	if page := e.do(http.MethodGet, "/projects/p", nil).Body.String(); !strings.Contains(page, "stays where it is") {
		t.Error("the page should explain why it cannot move")
	}
}

func TestANodeWithAProjectOnItCannotBeRemoved(t *testing.T) {
	e := newNodesEnv(t)
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	n, _ := e.st.CreateNode(store.Node{Name: "friend", Endpoint: "ssh://u@100.64.0.5", State: store.NodeReady})
	_ = e.st.SetProjectNode(proj.ID, n.ID)
	rec := e.do(http.MethodPost, "/nodes/"+n.ID+"/delete", url.Values{})
	if !strings.Contains(rec.Header().Get("Location"), "still+has+a+project") {
		t.Fatalf("%s", rec.Header().Get("Location"))
	}
	if list, _ := e.st.ListNodes(); len(list) != 1 {
		t.Fatal("the node must stay")
	}
	_ = e.st.SetProjectNode(proj.ID, "")
	e.do(http.MethodPost, "/nodes/"+n.ID+"/delete", url.Values{})
	if list, _ := e.st.ListNodes(); len(list) != 0 {
		t.Fatal("an unused node can go")
	}
}

func TestANodeCanCarryAPrivateAddressForItsPorts(t *testing.T) {
	e := newNodesEnv(t)
	e.do(http.MethodPost, "/nodes", url.Values{"name": {"home"}, "endpoint": {"ssh://habib@dm-server"}, "address": {"192.168.100.94"}})
	list, _ := e.st.ListNodes()
	if len(list) != 1 || list[0].Address != "192.168.100.94" {
		t.Fatalf("%+v", list)
	}
	if page := e.do(http.MethodGet, "/nodes", nil).Body.String(); !strings.Contains(page, "Ports reached at") || !strings.Contains(page, "192.168.100.94") {
		t.Error("the page should show where the ports are reached")
	}
	if rec := e.do(http.MethodPost, "/nodes", url.Values{"name": {"bad"}, "endpoint": {"ssh://u@100.64.0.5"}, "address": {"1.2.3.4; rm -rf"}}); !strings.Contains(rec.Header().Get("Location"), "address+for+ports") {
		t.Errorf("a bad address must be refused: %s", rec.Header().Get("Location"))
	}
}

func TestDeletingAProjectRemovesItsContainers(t *testing.T) {
	e := newNodesEnv(t)
	rt := &fakeRuntime{}
	e.s.rt = rt
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	_, _ = e.st.CreateApp(store.App{ProjectID: proj.ID, Name: "web", Slug: "web", Port: 8080, Status: "running", Environment: "dev"})
	_, _ = e.st.CreateService(store.Service{ProjectID: proj.ID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: "dev", Port: 6379})

	rec := e.do(http.MethodPost, "/projects/p/delete", url.Values{})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "Project deleted.") {
		t.Fatalf("%d %s", rec.Code, rec.Header().Get("Location"))
	}
	got := strings.Join(rt.removed, ",")
	for _, want := range []string{"dm-web", "dm-svc-cache"} {
		if !strings.Contains(got, want) {
			t.Errorf("deleting a project must remove its container %s, removed: %s", want, got)
		}
	}
	if _, err := e.st.GetProjectBySlug(owner.ID, "p"); err == nil {
		t.Error("the project should be gone")
	}
}

func TestDeletingAProjectNamesContainersItCouldNotRemove(t *testing.T) {
	e := newNodesEnv(t)
	e.s.rt = &fakeRuntime{removeErr: errors.New("node unreachable")}
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	_, _ = e.st.CreateService(store.Service{ProjectID: proj.ID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: "dev", Port: 6379})
	rec := e.do(http.MethodPost, "/projects/p/delete", url.Values{})
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "could not be removed") || !strings.Contains(loc, "dm-svc-cache") {
		t.Fatalf("the message must name what is left running: %s", loc)
	}
}

func TestTheNodeListLeadsOnceThereAreNodesAndTheInstructionsLeadBefore(t *testing.T) {
	e := newNodesEnv(t)
	empty := e.do(http.MethodGet, "/nodes", nil).Body.String()
	if strings.Index(empty, "Connect a machine") > strings.Index(empty, "Your nodes") {
		t.Error("with no nodes the instructions come first")
	}
	_, _ = e.st.CreateNode(store.Node{Name: "friend", Endpoint: "ssh://u@100.64.0.5", State: store.NodeReady})
	page := e.do(http.MethodGet, "/nodes", nil).Body.String()
	if strings.Index(page, "Your nodes") > strings.Index(page, "Connect a machine") {
		t.Error("with nodes the list comes first")
	}
	if !strings.Contains(page, "A node sees everything that runs on it") {
		t.Error("the trust warning stays on the page")
	}
}
