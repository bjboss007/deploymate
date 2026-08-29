package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServiceDecl is one deploymate.yml services entry: a bare type
// ("postgres") or a pinned "type:tag" ("postgres:17"). Pin "" means the
// latest image of that type ("postgres:latest").
type ServiceDecl struct {
	Type string
	Pin  string
}

// Image returns the docker image the declaration runs: the pin when
// present, otherwise the type's latest tag.
func (d ServiceDecl) Image() string {
	if d.Pin == "" {
		return d.Type + ":latest"
	}
	return d.Type + ":" + d.Pin
}

// Manifest is the deploymate.yml contract: a repo declares the backing
// services its app needs, and every git deploy reconciles the project's
// services with the declaration. Declarative config must be deterministic:
// unknown types and malformed YAML fail the deploy, never silently ignore.
type Manifest struct {
	Services []string `yaml:"services"`
}

// ParseManifest validates a deploymate.yml document and returns the
// normalized, de-duplicated service declarations. `name` labels the file
// in error messages. Unknown top-level keys are ignored (forward
// compatibility); unknown types and bad pins are errors.
func ParseManifest(name string, data []byte) ([]ServiceDecl, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("malformed %s: %w", name, err)
	}
	seen := make(map[string]ServiceDecl, len(m.Services))
	out := make([]ServiceDecl, 0, len(m.Services))
	for _, raw := range m.Services {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		typ, pin, hasPin := strings.Cut(entry, ":")
		typ, pin = strings.TrimSpace(typ), strings.TrimSpace(pin)
		if hasPin && (pin == "" || strings.Contains(pin, ":") || strings.ContainsAny(pin, " \t/")) {
			return nil, fmt.Errorf("invalid service pin %q in %s (expected e.g. postgres:17 or redis:7-alpine)", entry, name)
		}
		if _, ok := ForType(typ); !ok {
			return nil, fmt.Errorf("unknown service type %q in %s (supported: postgres, mysql, redis)", typ, name)
		}
		decl := ServiceDecl{Type: typ, Pin: pin}
		if prev, dup := seen[typ]; dup {
			if prev.Pin != decl.Pin {
				return nil, fmt.Errorf("%s declares %q twice with different images — merge the entries", name, typ)
			}
			continue // exact duplicate collapses
		}
		seen[typ] = decl
		out = append(out, decl)
	}
	return out, nil
}

// LoadManifest reads the manifest for an app's environment: the base
// deploymate.yml plus an optional deploymate.{env}.yml overlay whose
// services list replaces the base's entirely (docker-compose list
// semantics — present file wins). A missing base or overlay is not an
// error; base-only is today's behavior.
func LoadManifest(checkoutDir, rootDir, env string) ([]ServiceDecl, error) {
	dir := filepath.Join(checkoutDir, rootDir)
	base := filepath.Join(dir, "deploymate.yml")
	overlay := filepath.Join(dir, "deploymate."+env+".yml")

	var decls []ServiceDecl
	if data, err := os.ReadFile(base); err == nil {
		decls, err = ParseManifest("deploymate.yml", data)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if data, err := os.ReadFile(overlay); err == nil {
		// Overlay replaces the base list — a staging app can drop a
		// production-only service or pin different images.
		decls, err = ParseManifest(filepath.Base(overlay), data)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return decls, nil
}

// DeclTypes returns the type list of declarations, for summaries.
func DeclTypes(decls []ServiceDecl) []string {
	out := make([]string, 0, len(decls))
	for _, d := range decls {
		out = append(out, d.Type)
	}
	return out
}
