package nodes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestEnsureKeyCreatesAUsableKeyOnceAndKeepsItPrivate(t *testing.T) {
	dir := t.TempDir()
	pub, err := EnsureKey(dir)
	if err != nil || !strings.HasPrefix(pub, "ssh-ed25519 ") || !strings.HasSuffix(pub, " deploymate") {
		t.Fatalf("%q %v", pub, err)
	}
	fi, err := os.Stat(KeyFile(dir))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the private key must be 0600: %v %v", fi, err)
	}
	if di, _ := os.Stat(filepath.Join(dir, "keys")); di.Mode().Perm() != 0o700 {
		t.Errorf("keys dir mode %v", di.Mode().Perm())
	}
	raw, _ := os.ReadFile(KeyFile(dir))
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatalf("OpenSSH must be able to read what we wrote: %v", err)
	}
	if got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))); !strings.HasPrefix(pub, got) {
		t.Fatalf("the public line does not match the private key")
	}
	again, _ := EnsureKey(dir)
	if again != pub {
		t.Fatal("a second call must return the same key, never replace it")
	}
}

var capGood = runtime.Capacity{CPUs: 16, MemBytes: 64 << 30, OS: "Ubuntu 24.04", Arch: "x86_64", DockerVersion: "28.5.2", GPURuntime: true}

func newNode(t *testing.T, st *store.Store, name string) store.Node {
	n, err := st.CreateNode(store.Node{Name: name, Endpoint: "ssh://u@100.64.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheckRecordsCapacityOrTheReason(t *testing.T) {
	st := testStore(t)
	n := newNode(t, st, "friend")
	if err := Check(context.Background(), st, n, func(context.Context, string) (runtime.Capacity, error) { return capGood, nil }); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetNode(n.ID)
	if got.State != store.NodeReady || got.CPUs != 16 || got.MemBytes != 64<<30 || !got.GPURuntime || got.LastSeenAt == "" || got.LastError != "" {
		t.Fatalf("%+v", got)
	}
	_ = Check(context.Background(), st, got, func(context.Context, string) (runtime.Capacity, error) {
		return runtime.Capacity{}, errors.New("ssh: Permission denied (publickey)")
	})
	got, _ = st.GetNode(n.ID)
	if got.State != store.NodeOffline || !strings.Contains(got.LastError, "Permission denied") || got.CPUs != 16 {
		t.Fatalf("a failed check marks it offline, says why, and keeps the last known capacity: %+v", got)
	}
}

func TestADrainingNodeStaysDrainingWhileItAnswers(t *testing.T) {
	st := testStore(t)
	n := newNode(t, st, "friend")
	_ = st.SetNodeDraining(n.ID, true)
	_ = Check(context.Background(), st, n, func(context.Context, string) (runtime.Capacity, error) { return capGood, nil })
	if got, _ := st.GetNode(n.ID); got.State != store.NodeDraining {
		t.Fatalf("a successful check must not undo the owner's drain: %s", got.State)
	}
}

func TestWatcherNeedsThreeFailuresInARowBeforeOffline(t *testing.T) {
	st := testStore(t)
	n := newNode(t, st, "friend")
	up := true
	w := NewWatcher(st, func(context.Context, string) (runtime.Capacity, error) {
		if up {
			return capGood, nil
		}
		return runtime.Capacity{}, errors.New("i/o timeout")
	}, time.Hour)
	ctx := context.Background()
	w.Once(ctx)
	if got, _ := st.GetNode(n.ID); got.State != store.NodeReady {
		t.Fatalf("first check: %s", got.State)
	}
	up = false
	w.Once(ctx)
	w.Once(ctx)
	if got, _ := st.GetNode(n.ID); got.State != store.NodeReady {
		t.Fatalf("two failures are a blip, not an outage: %s", got.State)
	}
	w.Once(ctx)
	if got, _ := st.GetNode(n.ID); got.State != store.NodeOffline || !strings.Contains(got.LastError, "i/o timeout") {
		t.Fatalf("third failure: %+v", got)
	}
	up = true
	w.Once(ctx)
	if got, _ := st.GetNode(n.ID); got.State != store.NodeReady {
		t.Fatalf("recovery: %s", got.State)
	}
}

func TestRealProbeNeedsTheKeyForSSHButNotForTCP(t *testing.T) {
	probe := RealProbe(t.TempDir())
	if _, err := probe(context.Background(), "ssh://u@100.64.0.5"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("%v", err)
	}
}
