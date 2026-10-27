package nodes

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// The real thing: a second machine reached over SSH. Set DEPLOYMATE_REAL_NODE=ssh://user@host (the user must be allowed to
// use Docker there) and, if needed, DEPLOYMATE_REAL_NODE_KEY=/path/to/private/key. It checks the connection, then places a
// tiny alpine container on that machine through the router, runs a command in it, and removes it again. It leaves no
// network behind and removes the image only if the test pulled it. This machine's Docker is the "local" side.
func TestPlacementOnARealSecondMachine(t *testing.T) {
	endpoint := os.Getenv("DEPLOYMATE_REAL_NODE")
	if endpoint == "" {
		t.Skip("set DEPLOYMATE_REAL_NODE=ssh://user@host to run against a real second machine")
	}
	key := os.Getenv("DEPLOYMATE_REAL_NODE_KEY")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	node, err := runtime.NewRemote(ctx, endpoint, key)
	if err != nil {
		t.Fatalf("cannot use Docker on the node: %v", err)
	}
	defer node.Close()
	c, err := node.Capacity(ctx)
	if err != nil {
		t.Fatalf("capacity: %v", err)
	}
	t.Logf("node: %d CPUs, %d MB, %s, %s, docker %s, nvidia runtime %v", c.CPUs, c.MemBytes>>20, c.OS, c.Arch, c.DockerVersion, c.GPURuntime)

	local, err := runtime.NewDocker()
	if err != nil {
		t.Skip("no local docker: ", err)
	}
	const image, name = "alpine:3.20", "dm-itest-realnode"
	if has, _ := local.HasImage(ctx, image); !has {
		if err := local.PullImage(ctx, image); err != nil {
			t.Fatal(err)
		}
	}
	hadImage, _ := node.HasImage(ctx, image)

	st := testStore(t)
	owner, _ := st.CreateUser(store.User{Email: "o@x.com", PasswordHash: "x", Role: "owner"})
	n, _ := st.CreateNode(store.Node{Name: "real", Endpoint: endpoint, State: store.NodeReady})
	proj, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	_ = st.SetProjectNode(proj.ID, n.ID)
	_, _ = st.CreateApp(store.App{ProjectID: proj.ID, Name: "w", Slug: "itest-real", Port: 8080, Status: "stopped", Environment: "production"})
	r := NewRouter(local, st, t.TempDir())
	r.dial = func(ctx context.Context, _ store.Node) (runtime.Runtime, error) { return runtime.NewRemote(ctx, endpoint, key) }
	defer r.Close()

	t.Cleanup(func() {
		bg := context.Background()
		_ = node.Remove(bg, name)
		if !hadImage {
			_, _ = node.RemoveImage(bg, image)
		}
	})
	if _, err := r.Create(ctx, runtime.Spec{Name: name, Image: image, Cmd: []string{"sleep", "120"}, Labels: map[string]string{"deploymate.app": "itest-real"}}); err != nil {
		t.Fatalf("create on the node: %v", err)
	}
	if err := r.Start(ctx, name); err != nil {
		t.Fatalf("start: %v", err)
	}
	info, err := r.Inspect(ctx, name)
	if err != nil || !info.Running {
		t.Fatalf("running on the node: %+v %v", info, err)
	}
	if _, err := local.Inspect(ctx, name); !errors.Is(err, runtime.ErrContainerNotFound) {
		t.Fatalf("it must not exist on this machine: %v", err)
	}
	arch, err := r.Exec(ctx, name, []string{"uname", "-m"})
	if err != nil || runtime.NormalizeArch(strings.TrimSpace(arch)) != runtime.NormalizeArch(c.Arch) {
		t.Fatalf("the container reports CPU %q, the node is %q (%v)", strings.TrimSpace(arch), c.Arch, err)
	}
	t.Logf("container ran on the node (uname -m = %s), image copied or pulled as needed", strings.TrimSpace(arch))
	if err := r.Remove(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := node.Inspect(ctx, name); !errors.Is(err, runtime.ErrContainerNotFound) {
		t.Errorf("removed from the node: %v", err)
	}
}

// On a real node without the NVIDIA runtime, asking for a GPU is refused with the reason before anything is created
// (nothing is pulled or started). Skipped when the node does have the runtime.
func TestGPURequestIsRefusedOnARealNodeWithoutOne(t *testing.T) {
	endpoint := os.Getenv("DEPLOYMATE_REAL_NODE")
	if endpoint == "" {
		t.Skip("set DEPLOYMATE_REAL_NODE=ssh://user@host to run against a real second machine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	node, err := runtime.NewRemote(ctx, endpoint, os.Getenv("DEPLOYMATE_REAL_NODE_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	if c, err := node.Capacity(ctx); err != nil || c.GPURuntime {
		t.Skip("this node has a GPU runtime")
	}
	_, err = node.Create(ctx, runtime.Spec{Name: "dm-gpu-refusal-test", Image: "alpine:3", GPU: true})
	if err == nil || !strings.Contains(err.Error(), "NVIDIA Container Toolkit") {
		t.Fatalf("err = %v", err)
	}
	t.Log(err)
}
