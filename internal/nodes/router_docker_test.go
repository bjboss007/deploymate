package nodes

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// A real placement: this machine's Docker is "local", a Docker-in-Docker daemon is the "node". An app of a pinned project
// must be created on the node (with its image copied over), run there, and be invisible to the local daemon. Needs Docker
// and pulls docker:dind, so only with DEPLOYMATE_DOCKER_TESTS=1.
func TestAPinnedAppRunsOnTheSecondDaemonNotThisOne(t *testing.T) {
	if os.Getenv("DEPLOYMATE_DOCKER_TESTS") != "1" {
		t.Skip("set DEPLOYMATE_DOCKER_TESTS=1 to run against a real Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	local, err := runtime.NewDocker()
	if err != nil {
		t.Skip("no docker: ", err)
	}
	const dind, endpoint = "dm-itest-dind", "tcp://127.0.0.1:12376"
	_ = exec.Command("docker", "rm", "-fv", dind).Run()
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", dind, "--privileged", "-e", "DOCKER_TLS_CERTDIR=",
		"-p", "127.0.0.1:12376:2375", "docker:27-dind").CombinedOutput(); err != nil {
		t.Fatalf("start the node daemon: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-fv", dind).Run() })
	var node *runtime.Docker
	for i := 0; ; i++ {
		if node, err = runtime.NewRemote(ctx, endpoint, ""); err == nil {
			break
		}
		if i > 40 {
			t.Fatalf("the node daemon never answered: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	defer node.Close()
	if has, _ := local.HasImage(ctx, "alpine:3.20"); !has {
		if err := local.PullImage(ctx, "alpine:3.20"); err != nil {
			t.Fatal(err)
		}
	}

	st := testStore(t)
	owner, _ := st.CreateUser(store.User{Email: "o@x.com", PasswordHash: "x", Role: "owner"})
	n, _ := st.CreateNode(store.Node{Name: "second-daemon", Endpoint: endpoint, State: store.NodeReady})
	proj, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	_ = st.SetProjectNode(proj.ID, n.ID)
	_, _ = st.CreateApp(store.App{ProjectID: proj.ID, Name: "web", Slug: "itest-web", Port: 8080, Status: "stopped", Environment: "production"})
	r := NewRouter(local, st, t.TempDir())
	defer r.Close()

	const name = "dm-itest-node-web"
	t.Cleanup(func() { _ = local.Remove(context.Background(), name) })
	spec := runtime.Spec{Name: name, Image: "alpine:3.20", Network: "dm-itest-net", Cmd: []string{"sleep", "300"},
		Labels: map[string]string{"deploymate.app": "itest-web"}}
	if _, err := r.Create(ctx, spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Start(ctx, name); err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := local.Inspect(ctx, name); !errors.Is(err, runtime.ErrContainerNotFound) {
		t.Fatalf("the container must not exist on this machine's daemon: %v", err)
	}
	info, err := node.Inspect(ctx, name)
	if err != nil || !info.Running {
		t.Fatalf("it must be running on the node: %+v %v", info, err)
	}
	if has, _ := node.HasImage(ctx, "alpine:3.20"); !has {
		t.Error("the image should have been copied to the node")
	}
	// Calls by name reach it through the router.
	viaRouter, err := r.Inspect(ctx, name)
	if err != nil || !viaRouter.Running {
		t.Fatalf("inspect through the router: %+v %v", viaRouter, err)
	}
	remoteHost, err := r.Exec(ctx, name, []string{"hostname"})
	nodeHost, _ := node.Exec(ctx, name, []string{"hostname"})
	if err != nil || strings.TrimSpace(remoteHost) == "" || strings.TrimSpace(remoteHost) != strings.TrimSpace(nodeHost) {
		t.Fatalf("exec through the router: %q vs %q (%v)", remoteHost, nodeHost, err)
	}
	if _, err := r.Stats(ctx, name); err != nil {
		t.Errorf("stats: %v", err)
	}
	if r.HostFor(name) != "127.0.0.1" {
		t.Errorf("HostFor: %s", r.HostFor(name))
	}

	if err := r.Remove(ctx, name); err != nil {
		t.Fatal(err)
	}
	if _, err := node.Inspect(ctx, name); !errors.Is(err, runtime.ErrContainerNotFound) {
		t.Errorf("removed from the node: %v", err)
	}
	if id, _ := st.ContainerNode(name); id != "" {
		t.Errorf("placement forgotten: %q", id)
	}
}
