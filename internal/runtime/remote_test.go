package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidEndpoint(t *testing.T) {
	for _, ok := range []string{"ssh://habib@100.64.0.5", "ssh://deploy_user@friend-pc.tail1234.ts.net:2222", "tcp://100.64.0.5:2375"} {
		if err := ValidEndpoint(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ssh://100.64.0.5", "ssh://-oProxyCommand=x@host", "ssh://u@-bad", "ssh://u@h;rm -rf", "ssh://u@h/path", "http://h:1", "tcp://h", "ssh://U@h"} {
		if err := ValidEndpoint(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestSSHArgsKeepTheHostAfterTheDoubleDash(t *testing.T) {
	got := strings.Join(sshArgs("habib", "100.64.0.5", ":2222", "/keys/k"), " ")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=accept-new", "-i /keys/k", "IdentitiesOnly=yes", "-p 2222", "-l habib -- 100.64.0.5 docker system dial-stdio"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
}

// The real dialer against the real local Docker: a stand-in "ssh" that runs `docker system dial-stdio` here proves the
// stdio connection speaks Docker's API (ping, info) without needing a second machine. Needs Docker.
func TestRemoteDockerOverTheStdioTransport(t *testing.T) {
	if os.Getenv("DEPLOYMATE_DOCKER_TESTS") != "1" {
		t.Skip("set DEPLOYMATE_DOCKER_TESTS=1 to run against a real Docker daemon")
	}
	fake := filepath.Join(t.TempDir(), "fake-ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec docker system dial-stdio\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEPLOYMATE_SSH", fake)
	d, err := NewRemote(context.Background(), "ssh://tester@100.64.0.5", "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := d.Capacity(context.Background())
	if err != nil || c.CPUs < 1 || c.MemBytes == 0 || c.DockerVersion == "" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Logf("node reports: %d CPUs, %d MB, %s, docker %s, nvidia runtime %v", c.CPUs, c.MemBytes>>20, c.OS, c.DockerVersion, c.GPURuntime)
	// A failing ssh surfaces its own complaint.
	bad := filepath.Join(t.TempDir(), "bad-ssh")
	_ = os.WriteFile(bad, []byte("#!/bin/sh\necho 'Permission denied (publickey).' >&2\nexit 255\n"), 0o755)
	t.Setenv("DEPLOYMATE_SSH", bad)
	if _, err := NewRemote(context.Background(), "ssh://tester@100.64.0.5", ""); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("the ssh error should reach the user: %v", err)
	}
}

func TestSSHFailuresSayWhatWentWrongAndWhatToDo(t *testing.T) {
	for stderr, wantHint := range map[string]string{
		"permission denied while trying to connect to the docker API at unix:///var/run/docker.sock": "docker group",
		"habib@100.64.0.5: Permission denied (publickey).":                                           "authorized_keys",
		"ssh: connect to host 100.64.0.5 port 22: Connection timed out":                              "Tailscale",
	} {
		fake := filepath.Join(t.TempDir(), "ssh")
		_ = os.WriteFile(fake, []byte("#!/bin/sh\necho '"+stderr+"' >&2\nexit 255\n"), 0o755)
		t.Setenv("DEPLOYMATE_SSH", fake)
		_, err := NewRemote(context.Background(), "ssh://u@100.64.0.5", "")
		if err == nil || !strings.Contains(err.Error(), stderr) || !strings.Contains(err.Error(), wantHint) {
			t.Errorf("%q: want the message and a hint about %q, got %v", stderr, wantHint, err)
		}
		if err != nil && strings.Contains(err.Error(), "Is the docker daemon running") {
			t.Errorf("the misleading Docker client text must not reach the user: %v", err)
		}
	}
}

func TestParseGPUInfo(t *testing.T) {
	got := ParseGPUInfo("NVIDIA GeForce RTX 4090, 24564\nNVIDIA GeForce RTX 3060, 12288\n")
	if got != "NVIDIA GeForce RTX 4090 24 GB, NVIDIA GeForce RTX 3060 12 GB" {
		t.Errorf("got %q", got)
	}
	for _, junk := range []string{"", "nvidia-smi: command not found", "x, notanumber"} {
		if ParseGPUInfo(junk) != "" {
			t.Errorf("%q should give nothing", junk)
		}
	}
}

func TestGPUProblemExplainsWhyNot(t *testing.T) {
	if gpuProblem(true, "Ubuntu 24.04") != "" {
		t.Error("a machine with the NVIDIA runtime has no problem")
	}
	if !strings.Contains(gpuProblem(false, "Docker Desktop"), "Mac or Windows") {
		t.Error("Docker Desktop needs its own explanation")
	}
	if !strings.Contains(gpuProblem(false, "Ubuntu 24.04"), "NVIDIA Container Toolkit") {
		t.Error("Linux without the runtime should be told what to install")
	}
}

// With a real Docker (DEPLOYMATE_DOCKER_TESTS=1): asking for a GPU on a machine without one fails before anything
// is created, with the reason. (A machine that does have an NVIDIA runtime skips this.)
func TestCreateRefusesAGPUWhereThereIsNone(t *testing.T) {
	if os.Getenv("DEPLOYMATE_DOCKER_TESTS") != "1" {
		t.Skip("set DEPLOYMATE_DOCKER_TESTS=1 to run against a real Docker daemon")
	}
	d, err := NewDocker()
	if err != nil {
		t.Skip(err)
	}
	c, err := d.Capacity(context.Background())
	if err != nil || c.GPURuntime {
		t.Skip("this Docker has a GPU runtime")
	}
	_, err = d.Create(context.Background(), Spec{Name: "dm-gpu-test", Image: "alpine:3", GPU: true})
	if err == nil || !strings.Contains(err.Error(), "GPU requested but") {
		t.Fatalf("err = %v", err)
	}
}
