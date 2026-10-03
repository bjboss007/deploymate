package stack

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		name  string
		app   store.App
		tile  string
		badge string
		label string
	}{
		{"prebuilt spring jar", store.App{Slug: "erp", DeployMode: store.DeployModeArtifact, Stack: "spring"}, "spring", "java", "Spring on Java 21"},
		{"detected react on a node runtime", store.App{Slug: "web", Runtime: "node:22", Stack: "react"}, "react", "node", "React on Node.js 22"},
		{"plain python runtime", store.App{Slug: "api", Runtime: "python:3.13"}, "python", "", "Python 3.13"},
		{"explicit override beats detection", store.App{Slug: "x", Runtime: "node:22", Stack: "react", Logo: "next"}, "next", "node", "Next.js on Node.js 22"},
		{"image app", store.App{Slug: "site", Image: "docker.io/library/nginx:alpine"}, "nginx", "", "nginx"},
		{"unknown image", store.App{Slug: "mystery", Image: "ghcr.io/acme/tool:1.2"}, "docker", "", "tool"},
		{"nothing known", store.App{Slug: "new"}, "docker", "", "Container"},
		{"bogus override ignored", store.App{Slug: "y", Runtime: "golang", Logo: "not-a-logo"}, "go", "", "Go"},
	} {
		id := Resolve(tc.app)
		if id.Tile != tc.tile || id.Badge != tc.badge || id.Label != tc.label {
			t.Errorf("%s: got tile=%q badge=%q label=%q, want %q %q %q", tc.name, id.Tile, id.Badge, id.Label, tc.tile, tc.badge, tc.label)
		}
	}
}

func TestAccentIsStableAndAvoidsStatusColours(t *testing.T) {
	a := store.App{Slug: "erp"}
	if AccentFor(a) != AccentFor(a) {
		t.Fatal("accent must be stable")
	}
	seen := map[string]bool{}
	for _, s := range Palette {
		seen[s.Hex] = true
		// No red/green/amber hues: those mean failed/healthy/warning.
		for _, bad := range []string{"#FF6B6B", "#3DDC97", "#FFB84D", "#E24B4A", "#639922"} {
			if strings.EqualFold(s.Hex, bad) {
				t.Errorf("palette colour %s collides with a status colour", s.Hex)
			}
		}
	}
	if len(seen) != len(Palette) {
		t.Error("palette colours must be distinct")
	}
	if got := AccentFor(store.App{Slug: "erp", Accent: "pink"}); got != "#D4537E" {
		t.Errorf("explicit accent = %s", got)
	}
	if got := AccentFor(store.App{Slug: "erp", Accent: "bogus"}); got != AccentFor(a) {
		t.Errorf("an unknown accent key must fall back to the automatic one, got %s", got)
	}
	if !ValidAccent("") || !ValidAccent("sky") || ValidAccent("hotpink") {
		t.Error("ValidAccent wrong")
	}
	// Names spread over the palette (not all the same colour).
	colours := map[string]bool{}
	for _, slug := range []string{"web-front", "py-api", "erp", "db-probe", "node-site", "vgg-app", "react-spa", "shortlink-web"} {
		colours[AccentFor(store.App{Slug: slug})] = true
	}
	if len(colours) < 4 {
		t.Errorf("only %d distinct colours across 8 apps", len(colours))
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectDir(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"react", map[string]string{"package.json": `{"dependencies":{"react":"^18","react-dom":"^18"}}`}, "react"},
		{"next beats react", map[string]string{"package.json": `{"dependencies":{"next":"14","react":"18"}}`}, "next"},
		{"vue via nuxt", map[string]string{"package.json": `{"dependencies":{"nuxt":"3"}}`}, "vue"},
		{"plain node", map[string]string{"package.json": `{"dependencies":{"express":"4"}}`}, ""},
		{"spring (gradle in a module)", map[string]string{"settings.gradle": "include 'app'", "app/build.gradle": "plugins { id 'org.springframework.boot' version '3.4.4' }"}, "spring"},
		{"spring (maven)", map[string]string{"pom.xml": "<artifactId>spring-boot-starter-web</artifactId>"}, "spring"},
		{"django", map[string]string{"requirements.txt": "Django==5.0\ngunicorn\n"}, "django"},
		{"fastapi", map[string]string{"pyproject.toml": "dependencies = [\n  \"fastapi>=0.1\",\n]"}, "fastapi"},
		{"laravel", map[string]string{"composer.json": `{"require":{"laravel/framework":"^11"}}`}, "laravel"},
		{"rails", map[string]string{"Gemfile": "gem 'rails', '~> 7'"}, "rails"},
		{"nothing recognisable", map[string]string{"main.go": "package main"}, ""},
	} {
		dir := t.TempDir()
		for n, b := range tc.files {
			write(t, dir, n, b)
		}
		if got := DetectDir(dir); got != tc.want {
			t.Errorf("%s: DetectDir = %q, want %q", tc.name, got, tc.want)
		}
	}
	if DetectDir(filepath.Join(t.TempDir(), "missing")) != "" {
		t.Error("a missing directory must detect nothing")
	}
}

func TestDetectJar(t *testing.T) {
	mk := func(names ...string) string {
		p := filepath.Join(t.TempDir(), "app.jar")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for _, n := range names {
			w, _ := zw.Create(n)
			_, _ = w.Write([]byte("x"))
		}
		zw.Close()
		f.Close()
		return p
	}
	if got := DetectJar(mk("META-INF/MANIFEST.MF", "BOOT-INF/lib/spring-boot-3.4.4.jar", "BOOT-INF/classes/a.class")); got != "spring" {
		t.Errorf("spring boot fat jar = %q", got)
	}
	if got := DetectJar(mk("META-INF/MANIFEST.MF", "com/acme/Main.class")); got != "" {
		t.Errorf("plain jar = %q", got)
	}
	if DetectJar(filepath.Join(t.TempDir(), "nope.jar")) != "" {
		t.Error("a missing jar detects nothing")
	}
}

func TestDetectedIgnoresTheOwnersChoice(t *testing.T) {
	app := store.App{Slug: "web", Runtime: "node:22", Stack: "react", Logo: "next"}
	id, fw := Detected(app)
	if id.Tile != "react" || !fw {
		t.Errorf("Detected = %q fw=%v, want react/true", id.Tile, fw)
	}
	if _, fw := Detected(store.App{Slug: "api", Runtime: "python:3.13"}); fw {
		t.Error("a runtime-only app has no detected framework")
	}
}
