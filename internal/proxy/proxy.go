// Package proxy generates Traefik routing labels for app containers.
//
// Traefik's docker provider discovers containers by label — no proxy
// restarts, no shared config files. DeployMate is the only writer of these
// labels; apps get a TLS router per domain with the configured Let's
// Encrypt resolver.
package proxy

import (
	"strconv"
	"strings"
)

// AppLabelsOpts configures one app container's Traefik labels.
type AppLabelsOpts struct {
	Slug       string   // app slug — the default router/service name
	RouterName string   // per-container router/service name; default Slug (blue/green stages set their own)
	Port       int      // the container's listening port (service port label)
	Domains    []string // Host rule domains; empty = not exposed
	LEResolver string   // certresolver name (see ResolverForLEMode)
	Priority   int64    // router priority when > 0 (blue/green: the newer container wins)
}

// AppLabels returns the Traefik labels for an app container. Empty when the
// app has no domains — unexposed containers stay unreachable.
func AppLabels(o AppLabelsOpts) map[string]string {
	if len(o.Domains) == 0 {
		return nil
	}
	name := o.RouterName
	if name == "" {
		name = o.Slug
	}
	labels := map[string]string{
		"traefik.enable": "true",
		"traefik.http.routers." + name + ".rule":       "Host(`" + strings.Join(o.Domains, "`, `") + "`)",
		"traefik.http.routers." + name + ".entrypoints": "websecure",
		"traefik.http.routers." + name + ".tls":         "true",
		"traefik.http.routers." + name + ".tls.certresolver": o.LEResolver,
	}
	if o.Priority > 0 {
		// The router with the higher priority wins for the same Host rule —
		// how a blue/green staged container takes over traffic at start.
		labels["traefik.http.routers."+name+".priority"] = strconv.FormatInt(o.Priority, 10)
	}
	if o.Port > 0 {
		labels["traefik.http.services."+name+".loadbalancer.server.port"] = strconv.Itoa(o.Port)
	}
	return labels
}

// ResolverForLEMode maps the config value to a Traefik certresolver name.
func ResolverForLEMode(mode string) string {
	if mode == "production" {
		return "letsencrypt"
	}
	return "staging"
}
