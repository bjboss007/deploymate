package appspec

import (
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func testApp() store.App {
	return store.App{Slug: "web", MemLimitMB: 128, CPULimit: 0.5}
}

// TestBuildSpecFullShape covers the canonical shape: PORT env, deploymate
// labels, Traefik labels with the slug router, publish, and limits.
func TestBuildSpecFullShape(t *testing.T) {
	spec := BuildSpec(testApp(), Options{
		Image:    "deploymate/apps/web:1",
		Name:     CanonicalName("web"),
		Port:     8080,
		HostPort: 27001,
		Env:      []string{"DATABASE_URL=x"},
		DeployID: "dep-1",
		Domains:  []string{"web.example.com"},
		LEResolver: "staging",
	})

	if spec.Name != "dm-web" || spec.Image != "deploymate/apps/web:1" {
		t.Fatalf("name/image = %q/%q", spec.Name, spec.Image)
	}
	if spec.Network != NetworkName {
		t.Fatalf("network = %q, want %q", spec.Network, NetworkName)
	}
	if spec.Port != 8080 || spec.HostPort != 27001 {
		t.Fatalf("ports = %d/%d, want 8080/27001", spec.Port, spec.HostPort)
	}
	if spec.MemLimitMB != 128 || spec.CPULimit != 0.5 {
		t.Fatalf("limits = %d/%v", spec.MemLimitMB, spec.CPULimit)
	}
	env := strings.Join(spec.Env, "\n")
	if !strings.Contains(env, "DATABASE_URL=x") || !strings.Contains(env, "PORT=8080") {
		t.Fatalf("env missing entries: %v", spec.Env)
	}
	if spec.Labels["deploymate.app"] != "web" || spec.Labels["deploymate.deploy"] != "dep-1" {
		t.Fatalf("deploymate labels = %v", spec.Labels)
	}
	if spec.Labels["deploymate.port"] != "8080" {
		t.Fatalf("deploymate.port = %v", spec.Labels["deploymate.port"])
	}
	if spec.Labels["traefik.http.routers.web.rule"] != "Host(`web.example.com`)" {
		t.Fatalf("router rule = %q", spec.Labels["traefik.http.routers.web.rule"])
	}
	if _, ok := spec.Labels["traefik.http.routers.web.priority"]; ok {
		t.Fatalf("unexpected priority label: %v", spec.Labels)
	}
}

// TestBuildSpecCarriesCommandOverrides proves the Entrypoint/Cmd options
// pass through to the runtime spec untouched.
func TestBuildSpecCarriesCommandOverrides(t *testing.T) {
	spec := BuildSpec(testApp(), Options{
		Image:      "img",
		Name:       CanonicalName("web"),
		Entrypoint: []string{"sleep"},
		Cmd:        []string{"3000"},
	})
	if len(spec.Entrypoint) != 1 || spec.Entrypoint[0] != "sleep" {
		t.Fatalf("entrypoint = %v, want [sleep]", spec.Entrypoint)
	}
	if len(spec.Cmd) != 1 || spec.Cmd[0] != "3000" {
		t.Fatalf("cmd = %v, want [3000]", spec.Cmd)
	}
	// Omitted options stay nil — the "image default" signal downstream.
	plain := BuildSpec(testApp(), Options{Image: "img", Name: CanonicalName("web")})
	if plain.Entrypoint != nil || plain.Cmd != nil {
		t.Fatalf("nil options must stay nil: %v / %v", plain.Entrypoint, plain.Cmd)
	}
}

// TestSplitArgs pins the whitespace-split contract of the deploy form
// fields: empty and whitespace-only input is nil ("image default"), runs
// of whitespace collapse, tabs/newlines split.
func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"\t\n", nil},
		{"sleep", []string{"sleep"}},
		{"sleep 3000", []string{"sleep", "3000"}},
		{"  httpd  -f\t-p 8080\n", []string{"httpd", "-f", "-p", "8080"}},
	}
	for _, c := range cases {
		got := SplitArgs(c.in)
		if got == nil && c.want != nil {
			t.Errorf("SplitArgs(%q) = nil, want %v", c.in, c.want)
			continue
		}
		if got != nil && (len(got) != len(c.want) || strings.Join(got, " ") != strings.Join(c.want, " ")) {
			t.Errorf("SplitArgs(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestBuildSpecStagedShape covers the blue/green shape: staged name, its
// own router + priority, and the temp host port.
func TestBuildSpecStagedShape(t *testing.T) {
	spec := BuildSpec(testApp(), Options{
		Image:      "deploymate/apps/web:2",
		Name:       StagedName("web", "dep-2"),
		Port:       8080,
		HostPort:   28001,
		DeployID:   "dep-2",
		Domains:    []string{"web.example.com"},
		RouterName: "web-dep-2",
		Priority:   1725000000000000000,
		LEResolver: "staging",
	})
	if spec.Name != "dm-web-dep-2" {
		t.Fatalf("staged name = %q", spec.Name)
	}
	if spec.Labels["traefik.http.routers.web-dep-2.priority"] != "1725000000000000000" {
		t.Fatalf("priority label = %q", spec.Labels["traefik.http.routers.web-dep-2.priority"])
	}
	if _, ok := spec.Labels["traefik.http.routers.web.rule"]; ok {
		t.Fatalf("staged spec must not carry the default slug router: %v", spec.Labels)
	}
	if spec.HostPort != 28001 {
		t.Fatalf("temp host port = %d", spec.HostPort)
	}
}

// TestBuildSpecPortless suppresses publish and PORT when there is no port.
func TestBuildSpecPortless(t *testing.T) {
	spec := BuildSpec(testApp(), Options{Image: "img", Name: "dm-web"})
	if spec.Port != 0 || spec.HostPort != 0 {
		t.Fatalf("portless spec publishes: %+v", spec)
	}
	for _, e := range spec.Env {
		if strings.HasPrefix(e, "PORT=") {
			t.Fatalf("portless spec carries PORT env: %v", spec.Env)
		}
	}
	if _, ok := spec.Labels["deploymate.port"]; ok {
		t.Fatalf("portless spec carries deploymate.port")
	}
}

// TestResolvedPreviewPort prefers the stored port and falls back to the
// per-slug hash.
func TestResolvedPreviewPort(t *testing.T) {
	app := store.App{Slug: "web"}
	if got := ResolvedPreviewPort(app); got != runtime.PreviewPort("web") {
		t.Fatalf("hash fallback = %d, want %d", got, runtime.PreviewPort("web"))
	}
	app.PreviewHostPort = 27123
	if got := ResolvedPreviewPort(app); got != 27123 {
		t.Fatalf("stored port = %d, want 27123", got)
	}
}
