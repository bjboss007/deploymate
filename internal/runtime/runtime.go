// Package runtime abstracts the compute substrate DeployMate runs apps on.
//
// Docker on the local host is the only implementation today. A remote-agent
// implementation (SSH/gRPC to another machine) is the multi-server path
// later — everything above this package talks to Runtime, never to Docker
// directly.
package runtime

import (
	"context"
	"errors"
	"hash/crc32"
	"io"
)

// ErrContainerNotFound is returned when a container name does not exist.
var ErrContainerNotFound = errors.New("runtime: container not found")

// Spec describes a container DeployMate wants to run. App containers never
// publish publicly-reachable host ports — only the reverse proxy (Traefik)
// does. HostPort, when set, publishes the container's Port to the HOST
// LOOPBACK only (127.0.0.1) so the dashboard's /preview proxy can reach the
// app from the host process — needed on Docker Desktop (bridge IPs are not
// host-reachable) and harmless on Linux servers.
type Spec struct {
	Name     string            // container name, e.g. "dm-myapp"
	Image    string            // image reference
	Env      []string          // KEY=VALUE pairs
	Labels   map[string]string // docker labels (Traefik routing, ownership)
	Network  string            // docker network to attach
	Binds    []string          // volume mounts, e.g. "vol-name:/data"
	Port     int               // the container's listening port (0 = none)
	HostPort int               // loopback-published host port for Port (0 = no publish)
}

// PreviewPort derives a stable loopback port for an app's preview URL.
func PreviewPort(slug string) int {
	return 20000 + int(crc32.ChecksumIEEE([]byte(slug))%30000)
}

// Info is a snapshot of a container's state.
type Info struct {
	ID       string
	Name     string
	Image    string
	Running  bool
	State    string
	Restarts int
}

// Runtime is the compute interface.
type Runtime interface {
	// EnsureNetwork creates the named bridge network if it does not exist.
	EnsureNetwork(ctx context.Context, name string) error
	// PullImage downloads an image from a registry.
	PullImage(ctx context.Context, image string) error
	// HasImage reports whether the image is already present locally. Pulling
	// unconditionally fails for local-only images (no registry copy), so
	// callers must check before pulling.
	HasImage(ctx context.Context, image string) (bool, error)
	// RemoveImage deletes a local image by tag. Best-effort: pruned images
	// are allowed to linger if the daemon refuses.
	RemoveImage(ctx context.Context, tag string) (bool, error)
	// Create makes a container without starting it; returns the container ID.
	Create(ctx context.Context, spec Spec) (string, error)
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, timeoutSec int) error
	// Remove deletes a container (stopping it first if needed).
	Remove(ctx context.Context, name string) error
	Inspect(ctx context.Context, name string) (Info, error)
	// Logs returns the multiplexed container log stream; callers demux with
	// docker's stdcopy. Follow keeps the stream open. Closing the reader
	// cancels a follow.
	Logs(ctx context.Context, name string, follow bool, tail int) (io.ReadCloser, error)
	// Exec runs a command inside a running container and returns combined
	// output; a non-zero exit code is an error.
	Exec(ctx context.Context, name string, cmd []string) (string, error)
	// Stats returns one resource sample for a running container.
	Stats(ctx context.Context, name string) (Stats, error)
	// StorageUsed reports the growable docker storage in bytes: images +
	// build cache (volumes are user data and excluded). The daemon cannot
	// report host-disk free space, so the alert is growth-based.
	StorageUsed(ctx context.Context) (uint64, error)
	Close() error
}

// Stats is one container resource sample.
type Stats struct {
	CPUPercent float64
	MemBytes   uint64
	NetRx      uint64 // cumulative bytes received
	NetTx      uint64 // cumulative bytes sent
}
