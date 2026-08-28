package builder

import (
	"fmt"
	"regexp"
	"strings"
)

// Runtime describes one selectable runtime environment. MiseName is the
// mise tool name used to pin a version (empty for versionless runtimes).
type Runtime struct {
	Key      string
	Label    string
	MiseName string
}

// Runtimes is the catalog offered in the UI, backed by Railpack providers.
var Runtimes = []Runtime{
	{"node", "Node.js", "node"},
	{"python", "Python", "python"},
	{"golang", "Go", "go"},
	{"ruby", "Ruby", "ruby"},
	{"php", "PHP", "php"},
	{"java", "Java", "java"},
	{"rust", "Rust", "rust"},
	{"deno", "Deno", "deno"},
	{"elixir", "Elixir", "elixir"},
	{"dotnet", ".NET", "dotnet"},
	{"staticfile", "Static site", ""},
}

// RuntimeByKey looks up a runtime by its key.
func RuntimeByKey(key string) (Runtime, bool) {
	for _, rt := range Runtimes {
		if rt.Key == key {
			return rt, true
		}
	}
	return Runtime{}, false
}

// RuntimeSpec is a parsed runtime value: the provider key plus an optional
// pinned version ("node:22" → Node.js 22, "python" → latest detected).
type RuntimeSpec struct {
	Key     string
	Version string
}

// ParseRuntimeSpec parses an apps.runtime value.
func ParseRuntimeSpec(s string) RuntimeSpec {
	key, version, _ := strings.Cut(strings.TrimSpace(s), ":")
	return RuntimeSpec{Key: key, Version: strings.TrimSpace(version)}
}

var versionRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// ValidVersion reports whether a version string is safe to write into a
// .mise.toml (letters, digits, dots, dashes, underscores).
func ValidVersion(v string) bool { return versionRe.MatchString(v) }

// ValidateRuntimeSpec checks a raw form value and returns a normalized
// spec or an error.
func ValidateRuntimeSpec(raw string) (string, error) {
	spec := ParseRuntimeSpec(raw)
	if spec.Key == "" {
		return "", nil // Dockerfile
	}
	rt, ok := RuntimeByKey(spec.Key)
	if !ok {
		return "", fmt.Errorf("unknown runtime %q", spec.Key)
	}
	if spec.Version != "" && !ValidVersion(spec.Version) {
		return "", fmt.Errorf("invalid version %q", spec.Version)
	}
	if spec.Version != "" && rt.MiseName == "" {
		return "", fmt.Errorf("runtime %q does not support version pinning", rt.Label)
	}
	if spec.Version == "" {
		return rt.Key, nil
	}
	return rt.Key + ":" + spec.Version, nil
}
