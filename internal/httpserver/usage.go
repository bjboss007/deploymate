package httpserver

import (
	"context"
	"sync"
	"time"

	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/usage"
)

const (
	usageTTL     = 15 * time.Second // project pages reload often; one probe per service per window
	usageTimeout = 4 * time.Second
)

type usageEntry struct {
	at  time.Time
	ips map[string]bool
	ok  bool
}

// serviceClients asks a running service who is connected to it (briefly
// cached). ok=false means unknown — probe failed or the type isn't probed.
func (s *Server) serviceClients(ctx context.Context, sv store.Service) (map[string]bool, bool) {
	s.usageMu.Lock()
	if e, hit := s.usageCache[sv.ID]; hit && time.Since(e.at) < usageTTL {
		s.usageMu.Unlock()
		return e.ips, e.ok
	}
	s.usageMu.Unlock()

	var creds map[string]string
	if enc, err := s.store.GetServiceCredentials(sv.ID); err == nil && len(enc) > 0 {
		creds = s.decryptCreds(enc)
	}
	pctx, cancel := context.WithTimeout(ctx, usageTimeout)
	defer cancel()
	ips, ok := usage.Clients(pctx, s.rt, sv.Type, services.ContainerName(sv.Slug), creds)

	s.usageMu.Lock()
	if s.usageCache == nil {
		s.usageCache = map[string]usageEntry{}
	}
	s.usageCache[sv.ID] = usageEntry{at: time.Now(), ips: ips, ok: ok}
	s.usageMu.Unlock()
	return ips, ok
}

// serviceUsage reports, per app and service, whether the app currently holds
// a connection: "connected", "idle" (given the URL but no connection seen
// right now) or "" (unknown). Services are probed in parallel; a stopped
// service or an unprobed type is unknown, never "idle".
func (s *Server) serviceUsage(ctx context.Context, apps []store.App, svcs []store.Service, excl map[string]map[string]bool) map[string]map[string]string {
	out := map[string]map[string]string{}
	if s.rt == nil {
		return out
	}
	// Each app's container addresses.
	appIPs := map[string]map[string]bool{}
	for _, a := range apps {
		set := map[string]bool{}
		for _, sl := range s.appSlots(a) {
			if info, err := s.rt.Inspect(ctx, sl.Name); err == nil {
				for _, ip := range info.IPs {
					set[ip] = true
				}
			}
		}
		appIPs[a.ID] = set
	}
	// Who is connected to each running service, in parallel.
	type result struct {
		ips map[string]bool
		ok  bool
	}
	res := make(map[string]result, len(svcs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, sv := range svcs {
		if sv.Status != "running" {
			continue
		}
		wg.Add(1)
		go func(sv store.Service) {
			defer wg.Done()
			ips, ok := s.serviceClients(ctx, sv)
			mu.Lock()
			res[sv.ID] = result{ips, ok}
			mu.Unlock()
		}(sv)
	}
	wg.Wait()

	for _, a := range apps {
		out[a.ID] = map[string]string{}
		if a.Status != "running" || len(appIPs[a.ID]) == 0 {
			continue // an app that isn't running has no connections to speak of
		}
		for _, sv := range svcs {
			if sv.Environment != a.Environment || excl[a.ID][sv.ID] {
				continue
			}
			r, probed := res[sv.ID]
			if !probed || !r.ok {
				continue
			}
			state := "idle"
			for ip := range appIPs[a.ID] {
				if r.ips[ip] {
					state = "connected"
					break
				}
			}
			out[a.ID][sv.ID] = state
		}
	}
	return out
}
