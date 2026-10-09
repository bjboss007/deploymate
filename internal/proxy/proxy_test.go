package proxy

import (
	"strings"
	"testing"
)

func TestAppLabels(t *testing.T) {
	labels := AppLabels(AppLabelsOpts{Slug: "mysite", Port: 8080, Domains: []string{"example.com", "www.example.com"}, LEResolver: "staging"})
	want := map[string]string{
		"traefik.enable":                                      "true",
		"traefik.http.routers.mysite.rule":                    "Host(`example.com`, `www.example.com`)",
		"traefik.http.routers.mysite.entrypoints":             "websecure",
		"traefik.http.routers.mysite.tls":                     "true",
		"traefik.http.routers.mysite.tls.certresolver":        "staging",
		"traefik.http.routers.mysite.service":                 ServiceName("mysite", 8080, ""),
		"traefik.http.services." + ServiceName("mysite", 8080, "") + ".loadbalancer.server.port": "8080",
	}
	for k, v := range want {
		if labels[k] != v {
			t.Errorf("label %s = %q, want %q", k, labels[k], v)
		}
	}
}

// TestAppLabelsRouterNameAndPriority covers the blue/green shape: a staged
// container names its router after itself and sets a priority so it wins
// the Host rule the moment it starts.
func TestAppLabelsRouterNameAndPriority(t *testing.T) {
	labels := AppLabels(AppLabelsOpts{
		Slug: "mysite", RouterName: "mysite-abc123", Port: 8080,
		Domains: []string{"example.com"}, LEResolver: "staging", Priority: 1725000000000000000,
	})
	for _, k := range []string{
		"traefik.http.routers.mysite-abc123.rule",
		"traefik.http.routers.mysite-abc123.tls.certresolver",
		"traefik.http.routers.mysite-abc123.priority",
		"traefik.http.routers.mysite-abc123.service",
	} {
		if labels[k] == "" {
			t.Errorf("missing label %s in %v", k, labels)
		}
	}
	if labels["traefik.http.routers.mysite-abc123.priority"] != "1725000000000000000" {
		t.Errorf("priority = %q", labels["traefik.http.routers.mysite-abc123.priority"])
	}
	// The default-slug router must NOT exist for the staged name.
	if _, ok := labels["traefik.http.routers.mysite.rule"]; ok {
		t.Errorf("unexpected default router label: %v", labels)
	}
}

func TestAppLabelsNoDomains(t *testing.T) {
	if labels := AppLabels(AppLabelsOpts{Slug: "mysite", Port: 8080}); labels != nil {
		t.Fatalf("expected no labels without domains, got %v", labels)
	}
}

func TestResolverForLEMode(t *testing.T) {
	if got := ResolverForLEMode("production"); got != "letsencrypt" {
		t.Errorf("production -> %q, want letsencrypt", got)
	}
	if got := ResolverForLEMode("staging"); got != "staging" {
		t.Errorf("staging -> %q, want staging", got)
	}
	if got := ResolverForLEMode("bogus"); got != "staging" {
		t.Errorf("unknown mode should default to staging, got %q", got)
	}
}

// Behind a tunnel or proxy (mode "off") there is no resolver: the router still
// terminates TLS on Traefik's default certificate, but never asks for one.
func TestNoCertResolverWhenOff(t *testing.T) {
	if got := ResolverForLEMode("off"); got != "" {
		t.Fatalf("off -> %q, want no resolver", got)
	}
	labels := AppLabels(AppLabelsOpts{Slug: "site", Domains: []string{"deploymate.link"}, Port: 80, LEResolver: ""})
	if _, has := labels["traefik.http.routers.site.tls.certresolver"]; has {
		t.Error("a certresolver label was written with no resolver")
	}
	if labels["traefik.http.routers.site.tls"] != "true" || labels["traefik.http.routers.site.entrypoints"] != "websecure" {
		t.Errorf("TLS termination labels missing: %v", labels)
	}
}

// TestAppLabelsReplicasShareService is the replica LB contract: two slots
// of the same deploy own distinct routers but point at ONE service whose
// labels are byte-identical — Traefik merges them into one backend. The
// healthcheck labels come from the app's health path.
func TestAppLabelsReplicasShareService(t *testing.T) {
	opts := func(router string) AppLabelsOpts {
		return AppLabelsOpts{Slug: "api", RouterName: router, Port: 8080, HealthPath: "/healthz",
			Domains: []string{"api.example.com"}, LEResolver: "staging", Priority: 42}
	}
	r1, r2 := AppLabels(opts("api-d1")), AppLabels(opts("api-d1-r2"))
	svc := ServiceName("api", 8080, "/healthz")
	if r1["traefik.http.routers.api-d1.service"] != svc || r2["traefik.http.routers.api-d1-r2.service"] != svc {
		t.Fatalf("routers must point at the shared service %s: %v / %v", svc, r1, r2)
	}
	if _, ok := r2["traefik.http.routers.api-d1.rule"]; ok {
		t.Error("slot 2 must not declare slot 1's router (a shared router name conflicts in Traefik)")
	}
	for _, k := range []string{".loadbalancer.server.port", ".loadbalancer.healthcheck.path",
		".loadbalancer.healthcheck.interval", ".loadbalancer.healthcheck.timeout"} {
		key := "traefik.http.services." + svc + k
		if r1[key] == "" || r1[key] != r2[key] {
			t.Errorf("service label %s must be present and identical: %q vs %q", key, r1[key], r2[key])
		}
	}
	if r1["traefik.http.services."+svc+".loadbalancer.healthcheck.path"] != "/healthz" {
		t.Error("healthcheck path must come from the app's health path")
	}
}

// TestServiceNameIsConfigAddressed: a different port or health path yields a
// different service — never a conflicting redefinition of the same name
// (which makes Traefik drop the service: spike 2c).
func TestServiceNameIsConfigAddressed(t *testing.T) {
	base := ServiceName("api", 8080, "/")
	if ServiceName("api", 8080, "/") != base {
		t.Fatal("service name must be deterministic")
	}
	if ServiceName("api", 9090, "/") == base || ServiceName("api", 8080, "/healthz") == base {
		t.Error("port or health path change must change the service name")
	}
	if ServiceName("web", 8080, "/") == base {
		t.Error("service names must be per app")
	}
}

func TestAppLabelsNoHealthPathNoHealthcheck(t *testing.T) {
	labels := AppLabels(AppLabelsOpts{Slug: "s", Port: 80, Domains: []string{"s.dev"}})
	for k := range labels {
		if strings.Contains(k, "healthcheck") {
			t.Errorf("unexpected healthcheck label %s without a health path", k)
		}
	}
}
