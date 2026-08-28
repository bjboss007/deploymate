package proxy

import "testing"

func TestAppLabels(t *testing.T) {
	labels := AppLabels("mysite", 8080, []string{"example.com", "www.example.com"}, "staging")
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

func TestAppLabelsNoDomains(t *testing.T) {
	if labels := AppLabels("mysite", 8080, nil, "staging"); labels != nil {
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
