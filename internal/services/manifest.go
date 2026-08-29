package services

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is the deploymate.yml contract: a repo declares the backing
// services its app needs, and every git deploy reconciles the project's
// services with the declaration. Declarative config must be deterministic:
// unknown types and malformed YAML fail the deploy, never silently ignore.
type Manifest struct {
	Services []string `yaml:"services"`
}

// ParseManifest validates a deploymate.yml document and returns the
// normalized, de-duplicated service types it declares. Unknown top-level
// keys are ignored (forward compatibility); unknown types are errors.
func ParseManifest(data []byte) ([]string, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("malformed deploymate.yml: %w", err)
	}
	seen := make(map[string]bool, len(m.Services))
	out := make([]string, 0, len(m.Services))
	for _, raw := range m.Services {
		typ := strings.TrimSpace(raw)
		if typ == "" {
			continue
		}
		if _, ok := ForType(typ); !ok {
			return nil, fmt.Errorf("unknown service type %q in deploymate.yml (supported: postgres, mysql, redis)", typ)
		}
		if !seen[typ] {
			seen[typ] = true
			out = append(out, typ)
		}
	}
	return out, nil
}

// LoadManifest reads deploymate.yml from the app's source directory (the
// repo root when rootDir is empty). A missing file is not an error — it
// means "no declared services", today's behavior.
func LoadManifest(checkoutDir, rootDir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(checkoutDir, rootDir, "deploymate.yml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseManifest(data)
}
