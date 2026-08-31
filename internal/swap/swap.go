// Package swap orchestrates zero-downtime container swaps: the caller
// creates a staged container (new image, temp loopback host port, its own
// Traefik router with a higher priority), then Swap starts it, probes it,
// flips the stored preview port, removes the old container, and renames the
// staged one to the canonical name. A failed probe removes the staged
// container and leaves the old one serving — a failed deploy never takes
// the app down.
//
// Ordering is deliberate: OnFlip runs BEFORE the old container is removed,
// so preview traffic (which resolves the stored port per request) never
// hits a refused port; the old port serves until the store flips, then the
// probed temp port serves. The millisecond gap between Remove and Rename —
// when no container carries the canonical name — is already tolerated by
// the logs SSE panel and the monitor's probes.
package swap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
)

// ErrStagedFailed reports that the staged container never became healthy;
// the old container and the store are untouched.
var ErrStagedFailed = errors.New("swap: staged container failed readiness probe")

// Options tweaks the swap; zero values pick the defaults.
type Options struct {
	// OnFlip persists the new host port (or clears it for port-0 apps)
	// once the staged container is proven healthy. Failure aborts the swap
	// with the old container untouched.
	OnFlip func(hostPort int) error
	// ProbeURL builds the readiness URL for a host port. Default:
	// http://127.0.0.1:%d/ (the monitor's probe semantics).
	ProbeURL      func(hostPort int) string
	ProbeAttempts int           // default 30
	ProbeInterval time.Duration // default 2s
}

// ReserveLoopbackPort grabs a free loopback port for the staged container.
// The port is released before Create — another process can steal it in the
// window (documented race; acceptable for one maintainer, and the probe
// would catch a wrong container quickly).
func ReserveLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback port: %w", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// Swap moves a staged container into the canonical slot. The caller has
// already Created spec.Name; Swap starts, probes, flips, and renames it.
// canonical is the name monitor/logs/Traefik follow (appspec.CanonicalName).
func Swap(ctx context.Context, rt runtime.Runtime, canonical string, spec runtime.Spec, o Options) error {
	attempts, interval := o.ProbeAttempts, o.ProbeInterval
	if attempts == 0 {
		attempts = 30
	}
	if interval == 0 {
		interval = 2 * time.Second
	}
	probeURL := o.ProbeURL
	if probeURL == nil {
		probeURL = func(port int) string { return fmt.Sprintf("http://127.0.0.1:%d/", port) }
	}

	if err := rt.Start(ctx, spec.Name); err != nil {
		return fmt.Errorf("start staged container: %w", err)
	}

	// Probe the staged container on its temp host port. Port-0 apps publish
	// nothing and skip the probe — the swap is start→remove→rename only.
	if spec.HostPort > 0 {
		if err := probeReady(ctx, probeURL(spec.HostPort), attempts, interval); err != nil {
			_ = rt.Remove(ctx, spec.Name)
			return fmt.Errorf("%w: %v", ErrStagedFailed, err)
		}
	}

	// Flip the store first (see package comment for the ordering rationale).
	if o.OnFlip != nil {
		if err := o.OnFlip(spec.HostPort); err != nil {
			_ = rt.Remove(ctx, spec.Name)
			return fmt.Errorf("flip preview port: %w", err)
		}
	}

	// Drop the old container; a first deploy has none.
	if err := rt.Remove(ctx, canonical); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return fmt.Errorf("remove old container: %w", err)
	}

	// The staged container takes the canonical name. Labels and restart
	// policy carry over, so monitor stats, logs, and Traefik keep working.
	if err := rt.Rename(ctx, spec.Name, canonical); err != nil {
		return fmt.Errorf("rename staged container: %w", err)
	}
	return nil
}

func probeReady(ctx context.Context, url string, attempts int, interval time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return lastErr
}
