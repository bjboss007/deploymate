// Package nodes keeps track of worker machines: the SSH key this server uses to reach them, a check that asks a
// node's Docker what it has, and a loop that keeps each node's state current. Placement (which app runs where) comes
// later; see docs/specs/shared-compute.md.
package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

const keyName = "node_ed25519"

// KeyFile is where this server's node key lives under the data directory (the keys folder is mode 0700).
func KeyFile(dataDir string) string { return filepath.Join(dataDir, "keys", keyName) }

// EnsureKey creates the SSH key this server uses to reach nodes, if it is not there yet, and returns its public line
// ("ssh-ed25519 AAAA… deploymate") to paste into the node user's authorized_keys.
func EnsureKey(dataDir string) (string, error) {
	path := KeyFile(dataDir)
	if raw, err := os.ReadFile(path + ".pub"); err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "deploymate")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " deploymate"
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	return line, nil
}

// Probe connects to a node's Docker and reports what the machine has.
func Probe(ctx context.Context, endpoint, keyFile string) (runtime.Capacity, error) {
	d, err := runtime.NewRemote(ctx, endpoint, keyFile)
	if err != nil {
		return runtime.Capacity{}, err
	}
	defer d.Close()
	return d.Capacity(ctx)
}

// ProbeFunc is Probe's shape, so tests can stand in for the network.
type ProbeFunc func(ctx context.Context, endpoint string) (runtime.Capacity, error)

// Check probes one node and records the result.
func Check(ctx context.Context, st *store.Store, n store.Node, probe ProbeFunc) error {
	c, err := probe(ctx, n.Endpoint)
	if err != nil {
		msg := short(err.Error())
		_ = st.SetNodeProbe(n.ID, false, store.Node{}, msg)
		return err
	}
	return st.SetNodeProbe(n.ID, true, store.Node{CPUs: c.CPUs, MemBytes: c.MemBytes, OS: c.OS, Arch: c.Arch, DockerVersion: c.DockerVersion, GPURuntime: c.GPURuntime, GPUInfo: c.GPUInfo}, "")
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Watcher re-checks every node on an interval. A node must fail failuresToOffline checks in a row before it is shown
// offline, so one dropped packet does not flap its state.
type Watcher struct {
	st       *store.Store
	probe    ProbeFunc
	interval time.Duration
	mu       sync.Mutex
	fails    map[string]int
}

const failuresToOffline = 3

func NewWatcher(st *store.Store, probe ProbeFunc, interval time.Duration) *Watcher {
	return &Watcher{st: st, probe: probe, interval: interval, fails: map[string]int{}}
}

// Run checks all nodes now and then every interval until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	w.Once(ctx)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Once(ctx)
		}
	}
}

// Once checks every node a single time.
func (w *Watcher) Once(ctx context.Context) {
	list, err := w.st.ListNodes()
	if err != nil {
		slog.Error("nodes: list", "err", err)
		return
	}
	var wg sync.WaitGroup
	for _, n := range list {
		wg.Add(1)
		go func(n store.Node) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			c, err := w.probe(cctx, n.Endpoint)
			w.mu.Lock()
			defer w.mu.Unlock()
			if err != nil {
				w.fails[n.ID]++
				// Keep the last good state until the failures add up; always record why.
				if w.fails[n.ID] >= failuresToOffline || n.State == store.NodeOffline {
					_ = w.st.SetNodeProbe(n.ID, false, store.Node{}, short(err.Error()))
				}
				slog.Warn("node check failed", "node", n.Name, "fails", w.fails[n.ID], "err", err)
				return
			}
			w.fails[n.ID] = 0
			_ = w.st.SetNodeProbe(n.ID, true, store.Node{CPUs: c.CPUs, MemBytes: c.MemBytes, OS: c.OS, Arch: c.Arch, DockerVersion: c.DockerVersion, GPURuntime: c.GPURuntime, GPUInfo: c.GPUInfo}, "")
		}(n)
	}
	wg.Wait()
}

// ErrNoKey is returned when the SSH key has not been created yet.
var ErrNoKey = errors.New("this server has no node key yet: create one on the Nodes page first")

// RealProbe returns a ProbeFunc that uses the server's own key.
func RealProbe(dataDir string) ProbeFunc {
	return func(ctx context.Context, endpoint string) (runtime.Capacity, error) {
		key := ""
		if strings.HasPrefix(endpoint, "ssh://") {
			key = KeyFile(dataDir)
			if _, err := os.Stat(key); err != nil {
				return runtime.Capacity{}, ErrNoKey
			}
		}
		c, err := Probe(ctx, endpoint, key)
		if err == nil && c.GPURuntime {
			c.GPUInfo = runtime.GPUInfoOver(ctx, endpoint, key)
		}
		return c, err
	}
}
