package proxy

import "testing"

func TestAppLabels(t *testing.T) {
	labels := AppLabels(AppLabelsOpts{Slug: "mysite", Port: 8080, Domains: []string{"example.com", "www.example.com"}, LEResolver: "staging"})
	want := map[string]string{
		"traefik.enable":                                      "true",
		"traefik.http.routers.mysite.rule":                    "Host(`example.com`, `www.example.com`)",
		"traefik.http.routers.mysite.entrypoints":             "websecure",
		"traefik.http.routers.mysite.tls":                     "true",
		"traefik.http.routers.mysite.tls.certresolver":        "staging",
		"traefik.http.services.mysite.loadbalancer.server.port": "8080",
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
		"traefik.http.services.mysite-abc123.loadbalancer.server.port",
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
