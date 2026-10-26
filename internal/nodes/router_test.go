package nodes

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeRT embeds the interface so only what a test needs is implemented; anything else panics, which flags a call that
// went to the wrong machine.
type fakeRT struct {
	runtime.Runtime
	id       string
	images   map[string]bool
	networks []string
	created  []runtime.Spec
	calls    []string
	loaded   []string
	pulled   []string
	arch     string // "" = the check is skipped for this side
	pullErr  error
}

func newFake(id string, images ...string) *fakeRT {
	f := &fakeRT{id: id, images: map[string]bool{}}
	for _, i := range images {
		f.images[i] = true
	}
	return f
}
func (f *fakeRT) HasImage(_ context.Context, i string) (bool, error) { return f.images[i], nil }
func (f *fakeRT) PullImage(_ context.Context, i string) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	f.pulled = append(f.pulled, i)
	f.images[i] = true
	return nil
}
func (f *fakeRT) EnsureNetwork(_ context.Context, n string) error {
	f.networks = append(f.networks, n)
	return nil
}
func (f *fakeRT) Create(_ context.Context, s runtime.Spec) (string, error) {
	f.created = append(f.created, s)
	return f.id + "-cid", nil
}
func (f *fakeRT) Start(_ context.Context, n string) error {
	f.calls = append(f.calls, "start "+n)
	return nil
}
func (f *fakeRT) Remove(_ context.Context, n string) error {
	f.calls = append(f.calls, "remove "+n)
	return nil
}
func (f *fakeRT) Rename(_ context.Context, a, b string) error {
	f.calls = append(f.calls, "rename "+a+">"+b)
	return nil
}
func (f *fakeRT) Inspect(_ context.Context, n string) (runtime.Info, error) {
	f.calls = append(f.calls, "inspect "+n)
	return runtime.Info{Running: true}, nil
}
func (f *fakeRT) Exec(_ context.Context, n string, _ []string) (string, error) {
	f.calls = append(f.calls, "exec "+n)
	return f.id, nil
}
func (f *fakeRT) SaveImage(_ context.Context, ref string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("tar:" + ref)), nil
}
func (f *fakeRT) LoadImage(_ context.Context, r io.Reader) error {
	b, _ := io.ReadAll(r)
	f.loaded = append(f.loaded, string(b))
	f.images[strings.TrimPrefix(string(b), "tar:")] = true
	return nil
}
func (f *fakeRT) Close() error { return nil }

// ImageArch and HostArch let the architecture check run against the fakes.
func (f *fakeRT) ImageArch(context.Context, string) (string, error) { return f.arch, nil }
func (f *fakeRT) HostArch(context.Context) (string, error)          { return f.arch, nil }

type env struct {
	st          *store.Store
	local, node *fakeRT
	r           *Router
	n           store.Node
	pinned      store.App // app in a project pinned to the node
	plain       store.App // app in an unpinned project
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st := testStore(t)
	e := &env{st: st, local: newFake("local", "img:1"), node: newFake("node")}
	owner, _ := st.CreateUser(store.User{Email: "o@x.com", PasswordHash: "x", Role: "owner"})
	var err error
	if e.n, err = st.CreateNode(store.Node{Name: "friend", Endpoint: "ssh://u@100.64.0.5", State: store.NodeReady}); err != nil {
		t.Fatal(err)
	}
	pin, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "Pinned", Slug: "pinned"})
	_ = st.SetProjectNode(pin.ID, e.n.ID)
	pl, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "Plain", Slug: "plain"})
	e.pinned, _ = st.CreateApp(store.App{ProjectID: pin.ID, Name: "web", Slug: "web", Port: 8080, Status: "stopped", Environment: "production"})
	e.plain, _ = st.CreateApp(store.App{ProjectID: pl.ID, Name: "api", Slug: "api", Port: 8080, Status: "stopped", Environment: "production"})
	e.r = NewRouter(e.local, st, t.TempDir())
	e.r.dial = func(context.Context, store.Node) (runtime.Runtime, error) { return e.node, nil }
	return e
}

func appSpec(slug, name, image string) runtime.Spec {
	return runtime.Spec{Name: name, Image: image, Network: "deploymate-net", Port: 8080, HostPort: 20123,
		Labels: map[string]string{"deploymate.app": slug}}
}

func TestACreateForAPinnedProjectGoesToTheNodeWithItsImageAndNetwork(t *testing.T) {
	e := newEnv(t)
	cid, err := e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	if err != nil || cid != "node-cid" {
		t.Fatalf("%q %v", cid, err)
	}
	if len(e.local.created) != 0 || len(e.node.created) != 1 {
		t.Fatalf("local %d, node %d", len(e.local.created), len(e.node.created))
	}
	if len(e.node.networks) != 1 || e.node.networks[0] != "deploymate-net" {
		t.Errorf("the node needs the shared network: %v", e.node.networks)
	}
	if len(e.node.loaded) != 1 || e.node.loaded[0] != "tar:img:1" {
		t.Errorf("the image must be copied from this server: %v", e.node.loaded)
	}
	if got := e.node.created[0].HostIP; got != "100.64.0.5" {
		t.Errorf("published ports bind to the node's private address, got %q", got)
	}
	if e.r.HostFor("dm-web") != "100.64.0.5" || e.r.HostFor("dm-other") != "127.0.0.1" {
		t.Errorf("HostFor: %s / %s", e.r.HostFor("dm-web"), e.r.HostFor("dm-other"))
	}
	if id, _ := e.st.ContainerNode("dm-web"); id != e.n.ID {
		t.Errorf("the placement must be remembered: %q", id)
	}
}

func TestAnImageTheNodeAlreadyHasIsNotCopiedAndAPublicOneIsPulledThere(t *testing.T) {
	e := newEnv(t)
	e.node.images["img:1"] = true
	_, _ = e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	if len(e.node.loaded) != 0 {
		t.Errorf("already there: %v", e.node.loaded)
	}
	_, _ = e.r.Create(context.Background(), appSpec("web", "dm-web-2", "postgres:16")) // local does not have it either
	if len(e.node.pulled) != 1 || e.node.pulled[0] != "postgres:16" {
		t.Errorf("the node pulls what this server does not have: %v", e.node.pulled)
	}
}

func TestAnUnpinnedProjectStaysLocalAndUnlabelledContainersToo(t *testing.T) {
	e := newEnv(t)
	if cid, _ := e.r.Create(context.Background(), appSpec("api", "dm-api", "img:1")); cid != "local-cid" {
		t.Fatalf("%q", cid)
	}
	if cid, _ := e.r.Create(context.Background(), runtime.Spec{Name: "dm-buildkit", Image: "img:1"}); cid != "local-cid" {
		t.Fatalf("%q", cid)
	}
	if len(e.node.created) != 0 {
		t.Fatal("nothing belongs on the node")
	}
}

func TestADrainingOrOfflineNodeTakesNoNewWork(t *testing.T) {
	for _, state := range []string{store.NodeDraining, store.NodeOffline} {
		e := newEnv(t)
		_ = e.st.SetNodeDraining(e.n.ID, state == store.NodeDraining)
		if state == store.NodeOffline {
			_ = e.st.SetNodeProbe(e.n.ID, false, store.Node{}, "down")
		}
		_, err := e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
		if !errors.Is(err, ErrNodeNotAvailable) || !strings.Contains(err.Error(), "friend") {
			t.Errorf("%s: %v", state, err)
		}
		if len(e.node.created)+len(e.local.created) != 0 {
			t.Errorf("%s: a refused placement must not fall back to this server", state)
		}
	}
}

func TestCallsByNameFollowTheContainer(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, _ = e.r.Create(ctx, appSpec("web", "dm-web", "img:1"))
	_, _ = e.r.Create(ctx, appSpec("api", "dm-api", "img:1"))

	_, _ = e.r.Inspect(ctx, "dm-web")
	out, _ := e.r.Exec(ctx, "dm-web", []string{"true"})
	_, _ = e.r.Inspect(ctx, "dm-api")
	if out != "node" || strings.Join(e.node.calls, ",") != "inspect dm-web,exec dm-web" || strings.Join(e.local.calls, ",") != "inspect dm-api" {
		t.Fatalf("node %v, local %v, exec ran on %q", e.node.calls, e.local.calls, out)
	}

	// A swap renames the staged container to the canonical name: the placement moves with it.
	_ = e.r.Rename(ctx, "dm-web", "dm-web-live")
	if id, _ := e.st.ContainerNode("dm-web-live"); id != e.n.ID {
		t.Errorf("rename lost the placement: %q", id)
	}
	if id, _ := e.st.ContainerNode("dm-web"); id != "" {
		t.Errorf("the old name must be forgotten: %q", id)
	}
	_ = e.r.Remove(ctx, "dm-web-live")
	if id, _ := e.st.ContainerNode("dm-web-live"); id != "" || e.r.HostFor("dm-web-live") != "127.0.0.1" {
		t.Errorf("a removed container is forgotten")
	}
}

func TestPlacementSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	_, _ = e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	fresh := NewRouter(e.local, e.st, t.TempDir()) // a new process: empty cache, same database
	fresh.dial = func(context.Context, store.Node) (runtime.Runtime, error) { return e.node, nil }
	if _, err := fresh.Exec(context.Background(), "dm-web", nil); err != nil || len(e.node.calls) != 1 {
		t.Fatalf("%v %v", err, e.node.calls)
	}
}

func TestAnUnreachableNodeNamesItself(t *testing.T) {
	e := newEnv(t)
	e.r.dial = func(context.Context, store.Node) (runtime.Runtime, error) {
		return nil, errors.New("Permission denied (publickey)")
	}
	_, err := e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	if err == nil || !strings.Contains(err.Error(), "friend") || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("%v", err)
	}
}

func TestNodeAddress(t *testing.T) {
	for in, want := range map[string]string{
		"ssh://habib@100.64.0.5":      "100.64.0.5",
		"ssh://habib@100.64.0.5:2222": "100.64.0.5",
		"tcp://10.0.0.7:2375":         "10.0.0.7",
	} {
		if got, err := nodeAddress(in); err != nil || got != want {
			t.Errorf("%s: %q %v", in, got, err)
		}
	}
}

func TestAnImageBuiltForAnotherCPUIsNotCopiedTheNodePullsItsOwn(t *testing.T) {
	e := newEnv(t)
	e.local.arch, e.node.arch = "arm64", "x86_64"
	_, err := e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(e.node.loaded) != 0 {
		t.Fatalf("an arm64 image must never be copied to an amd64 node: %v", e.node.loaded)
	}
	if len(e.node.pulled) != 1 || e.node.pulled[0] != "img:1" {
		t.Fatalf("the node should pull its own: %v", e.node.pulled)
	}

	// An image only this server has (built here) cannot be pulled: the error must say what is wrong.
	e2 := newEnv(t)
	e2.local.arch, e2.node.arch = "arm64", "amd64"
	e2.node.pullErr = errors.New("manifest unknown")
	_, err = e2.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	if err == nil || !strings.Contains(err.Error(), "built for arm64") || !strings.Contains(err.Error(), "node runs amd64") {
		t.Fatalf("%v", err)
	}

	// Same CPU: copied as before.
	e3 := newEnv(t)
	e3.local.arch, e3.node.arch = "amd64", "x86_64"
	_, _ = e3.r.Create(context.Background(), appSpec("web", "dm-web", "img:1"))
	if len(e3.node.loaded) != 1 {
		t.Fatalf("matching CPUs copy the image: %v", e3.node.loaded)
	}
}

func TestANodeWhoseNameDoesNotResolveStillRunsContainersThatPublishNoPort(t *testing.T) {
	e := newEnv(t)
	_, _ = e.st.DB().Exec(`UPDATE nodes SET endpoint = 'ssh://u@some-ssh-alias.invalid' WHERE id = ?`, e.n.ID)
	spec := appSpec("web", "dm-web", "img:1")
	spec.HostPort, spec.Port = 0, 0
	if _, err := e.r.Create(context.Background(), spec); err != nil {
		t.Fatalf("a database or worker with no published port needs no address: %v", err)
	}
	spec.HostPort, spec.Port = 20123, 8080
	if _, err := e.r.Create(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "Tailscale IP") {
		t.Fatalf("a published port needs an address, and the error must say what to do: %v", err)
	}
	if got := e.r.HostFor("dm-web"); got != "some-ssh-alias.invalid" {
		t.Errorf("HostFor falls back to the name as written: %q", got)
	}
}

func TestAPrivateAddressOverridesTheEndpointForPorts(t *testing.T) {
	e := newEnv(t)
	_, _ = e.st.DB().Exec(`UPDATE nodes SET endpoint = 'ssh://u@some-ssh-alias.invalid', address = '192.168.100.94' WHERE id = ?`, e.n.ID)
	if _, err := e.r.Create(context.Background(), appSpec("web", "dm-web", "img:1")); err != nil {
		t.Fatalf("with a private address an ssh alias is fine: %v", err)
	}
	if got := e.node.created[0].HostIP; got != "192.168.100.94" || e.r.HostFor("dm-web") != "192.168.100.94" {
		t.Fatalf("ports bind and are reached at the private address, got %q / %q", got, e.r.HostFor("dm-web"))
	}
}
