package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A real build on a separate BuildKit daemon reached over TCP, the way a friend's machine would be. It starts
// moby/buildkit in a container (the "remote machine"), builds a tiny Dockerfile through it and checks both that
// the image came back and that the daemon did the work. Needs Docker, so only with DEPLOYMATE_DOCKER_TESTS=1.
func TestBuildRunsOnTheRemoteBuildKitAndTheImageComesBack(t *testing.T) {
	if os.Getenv("DEPLOYMATE_DOCKER_TESTS") != "1" {
		t.Skip("set DEPLOYMATE_DOCKER_TESTS=1 to run against a real Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	const name, port = "dm-itest-buildkitd", "127.0.0.1:12399"
	_ = exec.Command("docker", "rm", "-fv", name).Run()
	run := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	run("run", "-d", "--name", name, "--privileged", "-p", port+":1234", "moby/buildkit:latest", "--addr", "tcp://0.0.0.0:1234")
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-fv", name).Run()
		_ = exec.Command("docker", "buildx", "rm", "-f", remoteBuilder).Run()
		_ = exec.Command("docker", "rmi", "-f", "dm-itest-remote:1").Run()
	})
	for i := 0; ; i++ { // wait for the daemon to listen
		if dialable("tcp://"+port) == nil {
			break
		}
		if i > 30 {
			t.Fatal("buildkitd never listened")
		}
		time.Sleep(time.Second)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM alpine:3.20\nRUN echo built-remotely > /marker\nCMD [\"cat\", \"/marker\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteHost = "" })
	if err := SetRemoteHost("tcp://" + port); err != nil {
		t.Fatal(err)
	}
	var logs []string
	if err := Build(ctx, dir, "", "dm-itest-remote:1", func(l string) { logs = append(logs, l) }); err != nil {
		t.Fatalf("build: %v\n%s", err, strings.Join(logs, "\n"))
	}
	if !strings.Contains(strings.Join(logs, "\n"), "building on tcp://"+port) {
		t.Errorf("the log should say where the build ran:\n%s", strings.Join(logs, "\n"))
	}
	if got := strings.TrimSpace(run("run", "--rm", "dm-itest-remote:1")); got != "built-remotely" {
		t.Fatalf("the image that came back printed %q", got)
	}
	// The remote daemon holds the build's cache: proof the work happened there, not in the local daemon.
	if du := run("exec", name, "buildctl", "--addr", "tcp://127.0.0.1:1234", "du"); !strings.Contains(du, "Total:") || strings.Contains(du, "Total:\t0B") {
		t.Errorf("the remote daemon has no build cache, so the build did not run there:\n%s", du)
	}

	// The Railpack path (the usual one for apps without a Dockerfile) reaches the same daemon through BUILDKIT_HOST.
	// A static site keeps the test independent of distro package mirrors that Node builds pull from.
	if rp, err := exec.LookPath("railpack"); err == nil {
		app := t.TempDir()
		_ = os.WriteFile(filepath.Join(app, "index.html"), []byte("<h1>railpack-remote</h1>"), 0o644)
		before := run("exec", name, "buildctl", "--addr", "tcp://127.0.0.1:1234", "du")
		t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", "dm-itest-railpack:1").Run() })
		var rlogs []string
		if err := BuildRailpack(ctx, rp, app, "dm-itest-railpack:1", RuntimeSpec{Key: "staticfile"}, func(l string) { rlogs = append(rlogs, l) }); err != nil {
			if strings.Contains(strings.Join(rlogs, "\n")+err.Error(), "no space left on device") {
				t.Skip("the Docker disk is full: the Railpack path was not exercised")
			}
			t.Fatalf("railpack build: %v\n%s", err, strings.Join(rlogs, "\n"))
		}
		if !strings.Contains(strings.Join(rlogs, "\n"), "building on tcp://"+port) {
			t.Errorf("the railpack log should say where the build ran")
		}
		if id := strings.TrimSpace(run("image", "inspect", "dm-itest-railpack:1", "--format", "{{.Id}}")); id == "" {
			t.Fatal("the railpack image did not come back to the local daemon")
		}
		if after := run("exec", name, "buildctl", "--addr", "tcp://127.0.0.1:1234", "du"); after == before {
			t.Error("the remote daemon's cache did not change, so railpack did not build there")
		}
	} else {
		t.Log("railpack not installed: the Railpack path was not exercised")
	}
}
