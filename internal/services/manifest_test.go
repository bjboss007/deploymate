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
		want    []ServiceDecl
		wantErr string
	}{
		{
			name: "declared services",
			doc:  "services:\n  - postgres\n  - redis\n",
			want: []ServiceDecl{{Type: "postgres"}, {Type: "redis"}},
		},
		{
			name: "pinned versions",
			doc:  "services:\n  - postgres:17\n  - redis:7-alpine\n",
			want: []ServiceDecl{{Type: "postgres", Pin: "17"}, {Type: "redis", Pin: "7-alpine"}},
		},
		{
			name: "duplicates collapse",
			doc:  "services:\n  - postgres\n  - postgres\n  - redis\n",
			want: []ServiceDecl{{Type: "postgres"}, {Type: "redis"}},
		},
		{
			name: "identical pins collapse",
			doc:  "services:\n  - postgres:17\n  - postgres:17\n",
			want: []ServiceDecl{{Type: "postgres", Pin: "17"}},
		},
		{
			name: "blank entries skipped",
			doc:  "services:\n  - postgres\n  -\n  - \"\"\n",
			want: []ServiceDecl{{Type: "postgres"}},
		},
		{
			name: "unknown keys ignored for forward compat",
			doc:  "name: demo\nservices:\n  - redis\n",
			want: []ServiceDecl{{Type: "redis"}},
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
			name:    "unknown type with pin fails",
			doc:     "services:\n  - mongo:8\n",
			wantErr: "unknown service type \"mongo\"",
		},
		{
			// "postgres:" is a YAML mapping (key with null value), so the
			// YAML layer rejects it as malformed before pin validation.
			name:    "empty pin fails",
			doc:     "services:\n  - postgres:\n",
			wantErr: "malformed deploymate.yml",
		},
		{
			name:    "double colon fails",
			doc:     "services:\n  - postgres:17:alpine\n",
			wantErr: "invalid service pin",
		},
		{
			name:    "pin with whitespace fails",
			doc:     "services:\n  - postgres:17 alpine\n",
			wantErr: "invalid service pin",
		},
		{
			name:    "same type different pins fails",
			doc:     "services:\n  - postgres\n  - postgres:17\n",
			wantErr: "declares \"postgres\" twice with different images",
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
			got, err := ParseManifest("deploymate.yml", []byte(tc.doc))
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

func TestServiceDeclImage(t *testing.T) {
	if got := (ServiceDecl{Type: "postgres"}).Image(); got != "postgres:latest" {
		t.Fatalf("unpinned image = %q, want postgres:latest (no version → latest)", got)
	}
	if got := (ServiceDecl{Type: "postgres", Pin: "17"}).Image(); got != "postgres:17" {
		t.Fatalf("pinned image = %q, want postgres:17", got)
	}
	if got := (ServiceDecl{Type: "redis", Pin: "7-alpine"}).Image(); got != "redis:7-alpine" {
		t.Fatalf("pinned image = %q, want redis:7-alpine", got)
	}
}

func TestLoadManifest(t *testing.T) {
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	t.Run("absent means no services", func(t *testing.T) {
		got, err := LoadManifest(t.TempDir(), "", "production")
		if err != nil || len(got) != 0 {
			t.Fatalf("LoadManifest() = %v, %v; want none, nil", got, err)
		}
	})
	t.Run("base only", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "deploymate.yml"), "services:\n  - postgres\n")
		got, err := LoadManifest(dir, "", "staging")
		if err != nil || len(got) != 1 || got[0] != (ServiceDecl{Type: "postgres"}) {
			t.Fatalf("LoadManifest() = %v, %v", got, err)
		}
	})
	t.Run("overlay replaces base list", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "deploymate.yml"), "services:\n  - postgres\n  - redis\n")
		write(filepath.Join(dir, "deploymate.staging.yml"), "services:\n  - redis\n")
		got, err := LoadManifest(dir, "", "staging")
		if err != nil || len(got) != 1 || got[0] != (ServiceDecl{Type: "redis"}) {
			t.Fatalf("LoadManifest(staging) = %v, %v; want [redis] only (replacement)", got, err)
		}
	})
	t.Run("overlay alone is valid", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "deploymate.production.yml"), "services:\n  - postgres:17\n")
		got, err := LoadManifest(dir, "", "production")
		if err != nil || len(got) != 1 || got[0] != (ServiceDecl{Type: "postgres", Pin: "17"}) {
			t.Fatalf("LoadManifest() = %v, %v", got, err)
		}
	})
	t.Run("wrong env ignores overlay", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "deploymate.yml"), "services:\n  - postgres\n")
		write(filepath.Join(dir, "deploymate.staging.yml"), "services:\n  - redis\n")
		got, err := LoadManifest(dir, "", "production")
		if err != nil || len(got) != 1 || got[0].Type != "postgres" {
			t.Fatalf("LoadManifest(production) = %v, %v; want base [postgres]", got, err)
		}
	})
	t.Run("overlay error names the file", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "deploymate.yml"), "services:\n  - postgres\n")
		write(filepath.Join(dir, "deploymate.staging.yml"), "services: [\n")
		_, err := LoadManifest(dir, "", "staging")
		if err == nil || !strings.Contains(err.Error(), "deploymate.staging.yml") {
			t.Fatalf("LoadManifest() error = %v, want overlay filename", err)
		}
	})
	t.Run("rootDir join", func(t *testing.T) {
		dir := t.TempDir()
		write(filepath.Join(dir, "backend", "deploymate.yml"), "services:\n  - redis\n")
		got, err := LoadManifest(dir, "backend", "staging")
		if err != nil || len(got) != 1 || got[0].Type != "redis" {
			t.Fatalf("LoadManifest(rootDir) = %v, %v; want [redis]", got, err)
		}
	})
}
