// Package proxy generates Traefik routing labels for app containers.
//
// Traefik's docker provider discovers containers by label — no proxy
// restarts, no shared config files. DeployMate is the only writer of these
// labels; apps get a TLS router per domain with the configured Let's
// Encrypt resolver.
//
// Label scheme (replicas spike results, docs/specs/app-replicas.md):
// Traefik DROPS a router or service entirely — every request 404s — when
// two containers declare the same name with differing definitions. So:
//   - every container owns a UNIQUE router name (never shared), and
//   - its router points explicitly at a SHARED service whose name embeds a
//     hash of the service config (port + healthcheck). Containers with the
//     same config join one load-balanced service; a config change yields a
//     new service name instead of a conflicting redefinition.
//
// Routers with the same Host rule are disambiguated by priority (the newest
// deploy wins). When all routers point at the same service that choice is
// immaterial — the service round-robins every replica.
package proxy

import (
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"
)

// Traefik active healthcheck cadence for the shared service: a sick replica
// leaves rotation within one interval, and rejoins automatically when it
// passes again. A new server starts healthy (spike 2a), so a broken replica
// can receive traffic for up to one interval.
const (
	HealthcheckInterval = "10s"
	HealthcheckTimeout  = "3s"
)

// AppLabelsOpts configures one app container's Traefik labels.
type AppLabelsOpts struct {
	Slug       string   // app slug — the default router name and the service-name prefix
	RouterName string   // per-container router name; default Slug. Must be unique per container.
	Port       int      // the container's listening port (service port label)
	HealthPath string   // Traefik healthcheck path; "" = no healthcheck labels
	Domains    []string // Host rule domains; empty = not exposed
	LEResolver string   // certresolver name (see ResolverForLEMode)
	Priority   int64    // router priority when > 0 (the newest deploy wins the Host rule)
}

// ServiceName is the shared, config-addressed Traefik service for an app:
// slug + a short hash of everything the service labels declare. Two
// containers get the same name iff their service labels are identical.
func ServiceName(slug string, port int, healthPath string) string {
	sum := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%d|%s|%s|%s", port, healthPath, HealthcheckInterval, HealthcheckTimeout)))
	return fmt.Sprintf("%s-%08x", slug, sum)
}

// AppLabels returns the Traefik labels for an app container. Empty when the
// app has no domains — unexposed containers stay unreachable.
func AppLabels(o AppLabelsOpts) map[string]string {
	if len(o.Domains) == 0 {
		return nil
	}
	router := o.RouterName
	if router == "" {
		router = o.Slug
	}
	labels := map[string]string{
		"traefik.enable": "true",
		"traefik.http.routers." + router + ".rule":       "Host(`" + strings.Join(o.Domains, "`, `") + "`)",
		"traefik.http.routers." + router + ".entrypoints": "websecure",
		"traefik.http.routers." + router + ".tls":         "true",
		"traefik.http.routers." + router + ".tls.certresolver": o.LEResolver,
	}
	if o.Priority > 0 {
		// The router with the higher priority wins for the same Host rule —
		// how a new deploy's containers take over from the previous one.
		labels["traefik.http.routers."+router+".priority"] = strconv.FormatInt(o.Priority, 10)
	}
	if o.Port > 0 {
		svc := ServiceName(o.Slug, o.Port, o.HealthPath)
		labels["traefik.http.routers."+router+".service"] = svc
		labels["traefik.http.services."+svc+".loadbalancer.server.port"] = strconv.Itoa(o.Port)
		if o.HealthPath != "" {
			labels["traefik.http.services."+svc+".loadbalancer.healthcheck.path"] = o.HealthPath
			labels["traefik.http.services."+svc+".loadbalancer.healthcheck.interval"] = HealthcheckInterval
			labels["traefik.http.services."+svc+".loadbalancer.healthcheck.timeout"] = HealthcheckTimeout
		}
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
