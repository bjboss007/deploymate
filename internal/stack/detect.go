package stack

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxProbeBytes = 1 << 20

func read(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, maxProbeBytes)
	n, _ := f.Read(buf)
	return strings.ToLower(string(buf[:n]))
}

// DetectDir guesses the framework of a source tree from its manifests and
// returns a logo key ("react", "next", "spring", "django", …), or "" when it
// cannot tell. It looks at the root and one level of sub-directories (a
// multi-module Gradle build keeps its Spring app in a module). This is a
// best guess — the owner can always override it — and it never executes
// anything from the repository.
func DetectDir(dir string) string {
	roots := []string{dir}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "node_modules" && len(roots) < 24 {
				roots = append(roots, filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, r := range roots {
		if k := detectOne(r); k != "" {
			return k
		}
	}
	return ""
}

var (
	pyDjango  = regexp.MustCompile(`(?m)^\s*"?django\b`)
	pyFastapi = regexp.MustCompile(`(?m)^\s*"?fastapi\b`)
	pyFlask   = regexp.MustCompile(`(?m)^\s*"?flask\b`)
)

func detectOne(dir string) string {
	if pj := read(filepath.Join(dir, "package.json")); pj != "" {
		var m struct {
			Deps    map[string]json.RawMessage `json:"dependencies"`
			DevDeps map[string]json.RawMessage `json:"devDependencies"`
		}
		if json.Unmarshal([]byte(pj), &m) == nil {
			has := func(n string) bool { _, a := m.Deps[n]; _, b := m.DevDeps[n]; return a || b }
			switch {
			case has("next"):
				return "next"
			case has("nuxt"), has("vue"):
				return "vue"
			case has("@angular/core"):
				return "angular"
			case has("svelte"), has("@sveltejs/kit"):
				return "svelte"
			case has("react"), has("react-dom"):
				return "react"
			}
		}
	}
	for _, f := range []string{"pom.xml", "build.gradle", "build.gradle.kts"} {
		if b := read(filepath.Join(dir, f)); b != "" {
			if strings.Contains(b, "spring-boot") || strings.Contains(b, "org.springframework.boot") {
				return "spring"
			}
		}
	}
	for _, f := range []string{"requirements.txt", "pyproject.toml", "Pipfile"} {
		if b := read(filepath.Join(dir, f)); b != "" {
			switch {
			case pyDjango.MatchString(b):
				return "django"
			case pyFastapi.MatchString(b):
				return "fastapi"
			case pyFlask.MatchString(b):
				return "flask"
			}
		}
	}
	if b := read(filepath.Join(dir, "composer.json")); strings.Contains(b, "laravel/framework") {
		return "laravel"
	}
	if b := read(filepath.Join(dir, "Gemfile")); strings.Contains(b, "rails") {
		return "rails"
	}
	return ""
}

// DetectJar recognises a Spring Boot executable JAR from its entries: the
// fat jar carries spring-boot libraries (or the boot loader) inside. It
// reads only the zip's directory, never extracting or running anything.
func DetectJar(path string) string {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ""
	}
	defer zr.Close()
	for _, f := range zr.File {
		n := f.Name
		if strings.HasPrefix(n, "BOOT-INF/lib/spring-boot-") || strings.HasPrefix(n, "org/springframework/boot/loader/") ||
			strings.HasPrefix(n, "lib/spring-boot-") {
			return "spring"
		}
	}
	return ""
}

// IsJVMDir reports whether dir holds a Maven or Gradle project — the builds
// that need a JDK and a large heap, so building them on a small host is risky.
func IsJVMDir(dir string) bool {
	for _, f := range []string{"pom.xml", "build.gradle", "build.gradle.kts", "gradlew"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}
