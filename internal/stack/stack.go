// Package stack decides how an app is presented on its card: which
// technology logo it shows (React, Spring, Python, nginx, …), which runtime
// badge sits beside it (Node, Java, …), the one-line label ("Spring on Java
// 21"), and the app's identity colour. Everything is derived from what
// DeployMate already knows — the app's explicit logo choice, a framework
// detected at deploy time, its runtime setting, its image — with sensible
// fallbacks, so a card is never blank.
package stack

import (
	"hash/fnv"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/builder"
	"github.com/habibmuhammad/deploymate/internal/logos"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// Identity is how one app is drawn.
type Identity struct {
	Tile      string // logo key for the main tile
	Badge     string // logo key for the small corner badge ("" = none)
	Label     string // "Spring on Java 21"
	AccentKey string // palette key ("violet", …)
	Accent    string // identity colour, #RRGGBB
}

// frameworkRuntime says which runtime a framework runs on.
var frameworkRuntime = map[string]string{
	"spring": "java", "kotlin": "java",
	"react": "node", "next": "node", "vue": "node", "angular": "node", "svelte": "node",
	"django": "python", "fastapi": "python", "flask": "python",
	"laravel": "php", "rails": "ruby",
}

// runtimeLogo maps an apps.runtime key to a logo key.
var runtimeLogo = map[string]string{
	"node": "node", "python": "python", "golang": "go", "ruby": "ruby", "php": "php", "java": "java",
	"rust": "rust", "deno": "deno", "elixir": "elixir", "dotnet": "dotnet", "staticfile": "static",
}

// imageLogo maps well-known image names to a logo key.
var imageLogo = map[string]string{
	"nginx": "nginx", "node": "node", "python": "python", "redis": "redis", "postgres": "postgres",
	"mysql": "mysql", "mariadb": "mysql", "golang": "go", "php": "php", "ruby": "ruby", "rust": "rust",
	"openjdk": "java", "eclipse-temurin": "java", "httpd": "static", "caddy": "static",
}

// Resolve works out how to draw an app.
func Resolve(app store.App) Identity {
	id := Identity{AccentKey: AccentKeyFor(app), Accent: AccentFor(app)}

	// The runtime the app runs on, if we can tell.
	rt, rtVersion := "", ""
	if app.Runtime != "" {
		spec := builder.ParseRuntimeSpec(app.Runtime)
		rt, rtVersion = runtimeLogo[spec.Key], spec.Version
	}
	if rt == "" && app.DeployMode == store.DeployModeArtifact {
		rt, rtVersion = "java", builder.JavaMajor(app.Runtime) // prebuilt apps are JARs
	}
	imageKey := ""
	if rt == "" && app.Image != "" {
		imageKey = imageLogo[imageBase(app.Image)]
		if imageKey != "" {
			rt = imageKey
		}
	}

	// The framework: an explicit choice beats a detected one.
	fw := ""
	switch {
	case logos.Has(app.Logo):
		fw = app.Logo
	case logos.Has(app.Stack):
		fw = app.Stack
	}

	switch {
	case fw != "":
		id.Tile = fw
		if r, ok := frameworkRuntime[fw]; ok {
			if rt == "" || rt == r {
				rt = r
			}
		}
		if rt != "" && rt != fw {
			id.Badge = rt
		}
	case rt != "":
		id.Tile = rt
	default:
		id.Tile = "docker"
	}

	id.Label = label(id.Tile, rt, rtVersion, app.Image)
	return id
}

func label(tile, rt, rtVersion, image string) string {
	tileName := logos.Get(tile).Name
	if rt == "" || rt == tile {
		name := tileName
		if tile == "docker" && image != "" {
			return imageBase(image)
		}
		if rtVersion != "" && rt == tile {
			return name + " " + rtVersion
		}
		return name
	}
	r := logos.Get(rt).Name
	if rtVersion != "" {
		r += " " + rtVersion
	}
	return tileName + " on " + r
}

// imageBase strips the registry, namespace-less tag and digest: "docker.io/library/nginx:alpine" -> "nginx".
func imageBase(image string) string {
	if i := strings.IndexAny(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, "/"); i >= 0 {
		image = image[i+1:]
	}
	if i := strings.Index(image, ":"); i >= 0 {
		image = image[:i]
	}
	return strings.ToLower(image)
}

// Palette is the set of identity colours. It deliberately avoids red, green
// and amber: those already mean failed, healthy and warning, and an identity
// colour must never be mistaken for a state.
var Palette = []Swatch{
	{"violet", "Violet", "#7F77DD"},
	{"pink", "Pink", "#D4537E"},
	{"sky", "Sky", "#378ADD"},
	{"teal", "Teal", "#1D9E75"},
	{"coral", "Coral", "#D85A30"},
	{"indigo", "Indigo", "#5B6CE0"},
	{"magenta", "Magenta", "#B24BC6"},
	{"sand", "Sand", "#A68B5B"},
}

// Swatch is one identity colour.
type Swatch struct{ Key, Name, Hex string }

// AccentKeyFor is the palette key of the app's identity colour: its explicit
// choice, else a stable pick from its slug — the same app always gets the
// same colour.
func AccentKeyFor(app store.App) string {
	for _, s := range Palette {
		if s.Key == app.Accent {
			return s.Key
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(app.Slug))
	return Palette[int(h.Sum32())%len(Palette)].Key
}

// AccentFor is the identity colour as #RRGGBB.
func AccentFor(app store.App) string {
	k := AccentKeyFor(app)
	for _, s := range Palette {
		if s.Key == k {
			return s.Hex
		}
	}
	return Palette[0].Hex
}

// ValidAccent reports whether key is "" (automatic) or a palette key.
func ValidAccent(key string) bool {
	if key == "" {
		return true
	}
	for _, s := range Palette {
		if s.Key == key {
			return true
		}
	}
	return false
}
