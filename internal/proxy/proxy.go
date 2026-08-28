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

// AppLabels returns the Traefik labels for an app container. Empty when the
// app has no domains — unexposed containers stay unreachable.
func AppLabels(slug string, port int, domains []string, leResolver string) map[string]string {
	if len(domains) == 0 {
		return nil
	}
	labels := map[string]string{
		"traefik.enable": "true",
		"traefik.http.routers." + slug + ".rule":       "Host(`" + strings.Join(domains, "`, `") + "`)",
		"traefik.http.routers." + slug + ".entrypoints": "websecure",
		"traefik.http.routers." + slug + ".tls":         "true",
		"traefik.http.routers." + slug + ".tls.certresolver": leResolver,
	}
	if port > 0 {
		labels["traefik.http.services."+slug+".loadbalancer.server.port"] = strconv.Itoa(port)
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
