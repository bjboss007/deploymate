package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseManifest(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		want    []string
		wantErr string
	}{
		{
			name: "declared services",
			doc:  "services:\n  - postgres\n  - redis\n",
			want: []string{"postgres", "redis"},
		},
		{
			name: "duplicates collapse",
			doc:  "services:\n  - postgres\n  - postgres\n  - redis\n",
			want: []string{"postgres", "redis"},
		},
		{
			name: "blank entries skipped",
			doc:  "services:\n  - postgres\n  -\n  - \"\"\n",
			want: []string{"postgres"},
		},
		{
			name: "unknown keys ignored for forward compat",
			doc:  "name: demo\nservices:\n  - redis\n",
			want: []string{"redis"},
		},
		{
			name: "empty document means no services",
			doc:  "",
			want: nil,
		},
		{
			name: "null services means no services",
			doc:  "services:\n",
			want: nil,
		},
		{
			name:    "unknown type fails",
			doc:     "services:\n  - mongo\n",
			wantErr: "unknown service type \"mongo\"",
		},
		{
			name:    "malformed yaml fails",
			doc:     "services: [\n",
			wantErr: "malformed deploymate.yml",
		},
		{
			name:    "scalar services fails",
			doc:     "services: postgres\n",
			wantErr: "malformed deploymate.yml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseManifest([]byte(tc.doc))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseManifest() error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseManifest() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseManifest() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ParseManifest() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()

	// No file → no services, no error (today's behavior).
	got, err := LoadManifest(dir, "")
	if err != nil {
		t.Fatalf("LoadManifest() absent error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("LoadManifest() absent = %v, want none", got)
	}

	// Present file → parsed.
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	write(filepath.Join(dir, "deploymate.yml"), "services:\n  - postgres\n")
	got, err = LoadManifest(dir, "")
	if err != nil {
		t.Fatalf("LoadManifest() error = %v", err)
	}
	if len(got) != 1 || got[0] != "postgres" {
		t.Fatalf("LoadManifest() = %v, want [postgres]", got)
	}

	// root_directory join: manifest lives with the app's source.
	write(filepath.Join(dir, "backend", "deploymate.yml"), "services:\n  - redis\n")
	got, err = LoadManifest(dir, "backend")
	if err != nil {
		t.Fatalf("LoadManifest(rootDir) error = %v", err)
	}
	if len(got) != 1 || got[0] != "redis" {
		t.Fatalf("LoadManifest(rootDir) = %v, want [redis]", got)
	}
}
