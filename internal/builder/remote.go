package builder

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// A build host is another machine's BuildKit daemon (DEPLOYMATE_BUILD_HOST=tcp://100.64.0.5:1234, normally a
// Tailscale address). Builds run there, so a small server never has to hold the compiler; the finished image
// comes back and is loaded into the local daemon exactly like a local build.
//
// shortcut: one package-level host, set once at startup, and only tcp://. Several hosts and ssh:// come
// with the node scheduler (docs/specs/shared-compute.md).
var remoteHost string

const remoteBuilder = "dm-remote"

var hostRe = regexp.MustCompile(`^tcp://[A-Za-z0-9.-]+:[0-9]{1,5}$`)

// SetRemoteHost validates and sets the build host; "" turns remote builds off.
func SetRemoteHost(h string) error {
	if h != "" && !hostRe.MatchString(h) {
		return fmt.Errorf("DEPLOYMATE_BUILD_HOST must look like tcp://100.64.0.5:1234 (got %q)", h)
	}
	remoteHost = h
	return nil
}

// dialable is a var so tests can stand in for the network.
var dialable = func(host string) error {
	c, err := net.DialTimeout("tcp", strings.TrimPrefix(host, "tcp://"), 3*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// activeHost is the build host to use for this build, or "" to build here. An unreachable host never fails
// a deploy: it is reported in the build log and the build falls back to this server.
func activeHost(log func(string)) string {
	if remoteHost == "" {
		return ""
	}
	if err := dialable(remoteHost); err != nil {
		log(fmt.Sprintf("build host %s is not reachable (%v): building on this server instead", remoteHost, err))
		return ""
	}
	log("building on " + remoteHost)
	return remoteHost
}

// ensureRemoteBuilder points the buildx builder "dm-remote" at host (creating or re-pointing it).
func ensureRemoteBuilder(ctx context.Context, host string) error {
	if out, err := exec.CommandContext(ctx, "docker", "buildx", "inspect", remoteBuilder).CombinedOutput(); err == nil && strings.Contains(string(out), host) {
		return nil
	}
	_ = exec.CommandContext(ctx, "docker", "buildx", "rm", "-f", remoteBuilder).Run()
	if out, err := exec.CommandContext(ctx, "docker", "buildx", "create", "--name", remoteBuilder, "--driver", "remote", host).CombinedOutput(); err != nil {
		return fmt.Errorf("create the remote builder: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
