package swap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
)

// recordingRuntime records the swap's call sequence and can be armed to
// fail Remove (the "fresh deploy" case returns ErrContainerNotFound).
type recordingRuntime struct {
	mu       sync.Mutex
	started  []string
	stopped  []string
	removed  []string
	renamed  [][2]string
	removeErr error
	renameErr error
}

func (f *recordingRuntime) EnsureNetwork(context.Context, string) error           { return nil }
func (f *recordingRuntime) PullImage(context.Context, string) error               { return nil }
func (f *recordingRuntime) HasImage(context.Context, string) (bool, error)        { return true, nil }
func (f *recordingRuntime) RemoveImage(context.Context, string) (bool, error)     { return true, nil }
func (f *recordingRuntime) Create(context.Context, runtime.Spec) (string, error)  { return "cid", nil }
func (f *recordingRuntime) Start(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, name)
	return nil
}
func (f *recordingRuntime) Stop(_ context.Context, name string, timeout int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, fmt.Sprintf("%s:%d", name, timeout))
	return nil
}
func (f *recordingRuntime) Remove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return f.removeErr
}
func (f *recordingRuntime) Rename(_ context.Context, oldName, newName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renamed = append(f.renamed, [2]string{oldName, newName})
	return f.renameErr
}
func (f *recordingRuntime) Inspect(context.Context, string) (runtime.Info, error) { return runtime.Info{}, nil }
func (f *recordingRuntime) Logs(context.Context, string, bool, int) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *recordingRuntime) Exec(context.Context, string, []string) (string, error) { return "", nil }
func (f *recordingRuntime) ExecEnv(context.Context, string, []string, []string) (string, error) {
	return "", nil
}
func (f *recordingRuntime) WriteFile(context.Context, string, string, []byte) error { return nil }
func (f *recordingRuntime) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, nil
}
func (f *recordingRuntime) Stats(context.Context, string) (runtime.Stats, error)    { return runtime.Stats{}, nil }
func (f *recordingRuntime) StorageUsed(context.Context) (uint64, error)             { return 0, nil }
func (f *recordingRuntime) DiskUsage(context.Context) (runtime.DiskUsage, error)        { return runtime.DiskUsage{}, nil }
func (f *recordingRuntime) ImageSize(context.Context, string) (uint64, error)           { return 0, nil }
func (f *recordingRuntime) Close() error                                            { return nil }

func (f *recordingRuntime) calls() (started, removed []string, renamed [][2]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.started...), append([]string{}, f.removed...), append([][2]string{}, f.renamed...)
}

// readyServer returns a live httptest server Swap's probe can hit.
func readyServer() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	return srv
}

func stagedSpec(hostPort int) runtime.Spec {
	return runtime.Spec{Name: "dm-web-dep1", Image: "img", HostPort: hostPort}
}

// TestSwapSuccess proves the full sequence: Start staged → probe → OnFlip →
// Remove canonical → Rename staged→canonical, in that order.
func TestSwapSuccess(t *testing.T) {
	rt := &recordingRuntime{}
	srv := readyServer()
	defer srv.Close()
	var flipped int
	onFlip := func(hostPort int) error { flipped = hostPort; return nil }

	err := Swap(context.Background(), rt, "dm-web", stagedSpec(27001), Options{
		OnFlip:        onFlip,
		ProbeURL:      func(port int) string { return srv.URL },
		ProbeAttempts: 3,
		ProbeInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	started, removed, renamed := rt.calls()
	if len(started) != 1 || started[0] != "dm-web-dep1" {
		t.Fatalf("started = %v", started)
	}
	if len(removed) != 1 || removed[0] != "dm-web" {
		t.Fatalf("removed = %v", removed)
	}
	if len(renamed) != 1 || renamed[0] != [2]string{"dm-web-dep1", "dm-web"} {
		t.Fatalf("renamed = %v", renamed)
	}
	if flipped != 27001 {
		t.Fatalf("OnFlip port = %d, want 27001", flipped)
	}
}

// TestSwapProbeFailureRollsBack proves a staged container that never
// becomes healthy is removed with the old container and the store untouched
// (OnFlip never runs).
func TestSwapProbeFailureRollsBack(t *testing.T) {
	rt := &recordingRuntime{}
	onFlipCalled := false
	err := Swap(context.Background(), rt, "dm-web", stagedSpec(27001), Options{
		OnFlip:        func(int) error { onFlipCalled = true; return nil },
		ProbeURL:      func(port int) string { return "http://127.0.0.1:1/" }, // nothing listens
		ProbeAttempts: 2,
		ProbeInterval: time.Millisecond,
	})
	if !errors.Is(err, ErrStagedFailed) {
		t.Fatalf("err = %v, want ErrStagedFailed", err)
	}
	_, removed, renamed := rt.calls()
	if len(removed) != 1 || removed[0] != "dm-web-dep1" {
		t.Fatalf("staged not removed: %v", removed)
	}
	if len(renamed) != 0 {
		t.Fatalf("rename ran on failure: %v", renamed)
	}
	if onFlipCalled {
		t.Fatalf("OnFlip ran on failure")
	}
}

// TestSwapFreshDeploy tolerates a missing old container (first deploy).
func TestSwapFreshDeploy(t *testing.T) {
	rt := &recordingRuntime{removeErr: runtime.ErrContainerNotFound}
	srv := readyServer()
	defer srv.Close()
	err := Swap(context.Background(), rt, "dm-web", stagedSpec(27001), Options{
		OnFlip:        func(int) error { return nil },
		ProbeURL:      func(port int) string { return srv.URL },
		ProbeAttempts: 3,
		ProbeInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("swap (fresh): %v", err)
	}
	_, _, renamed := rt.calls()
	if len(renamed) != 1 || renamed[0] != [2]string{"dm-web-dep1", "dm-web"} {
		t.Fatalf("renamed = %v", renamed)
	}
}

// TestSwapPortlessSkipsProbe proves port-0 apps still swap (start, flip
// with 0, remove, rename) without probing anything.
func TestSwapPortlessSkipsProbe(t *testing.T) {
	rt := &recordingRuntime{}
	flipped := -1
	err := Swap(context.Background(), rt, "dm-web", runtime.Spec{Name: "dm-web-dep1"}, Options{
		OnFlip: func(hostPort int) error { flipped = hostPort; return nil },
	})
	if err != nil {
		t.Fatalf("swap (portless): %v", err)
	}
	if flipped != 0 {
		t.Fatalf("OnFlip port = %d, want 0", flipped)
	}
	_, _, renamed := rt.calls()
	if len(renamed) != 1 {
		t.Fatalf("renamed = %v, want the staged rename", renamed)
	}
}

// TestSwapDrainsOldContainer: after the flip the old container gets a
// graceful stop (SIGTERM + the drain window) BEFORE it is removed, so an
// app that shuts down cleanly finishes its in-flight requests.
func TestSwapDrainsOldContainer(t *testing.T) {
	srv := readyServer()
	defer srv.Close()
	rt := &recordingRuntime{}
	spec := runtime.Spec{Name: "dm-web-new", HostPort: 12345}
	err := Swap(context.Background(), rt, "dm-web", spec, Options{
		ProbeURL: func(int) string { return srv.URL }, ProbeAttempts: 1, ProbeInterval: time.Millisecond,
		DrainTimeoutSec: 7,
	})
	if err != nil {
		t.Fatalf("swap: %v", err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.stopped) != 1 || rt.stopped[0] != "dm-web:7" {
		t.Fatalf("stopped = %v, want the old container drained for 7s", rt.stopped)
	}
	if len(rt.removed) != 1 || rt.removed[0] != "dm-web" {
		t.Fatalf("removed = %v, want the old container after its drain", rt.removed)
	}
}
