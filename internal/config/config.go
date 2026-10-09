// Package config loads DeployMate configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds all runtime configuration. Everything is settable via
// environment variables so the same binary works in dev and on the server.
type Config struct {
	// Addr is the listen address for the HTTP server.
	Addr string
	// DataDir holds the SQLite database, encryption keys, build workspaces,
	// and git checkouts. Defaults to ./data in dev.
	DataDir string
	// KeyPath is the XChaCha20-Poly1305 master key file (0600).
	KeyPath string
	// SetupEmail/SetupPassword, when both set, create the initial owner user
	// at startup if no users exist yet.
	SetupEmail    string
	SetupPassword string
	// LEMode selects the Let's Encrypt resolver: "staging" (default),
	// "production", or "off" for a server behind a tunnel or proxy that serves
	// the real certificate (DeployMate then asks for none).
	LEMode string
	// RailpackPath is the railpack CLI to invoke for runtime builds.
	// Defaults to "railpack" (resolved via PATH); set it explicitly in dev
	// where go/bin etc. is not on the server's PATH.
	RailpackPath string
	// PreviewHost, when set (e.g. "dm.example.com"), gives every app a
	// public subdomain: {slug}.{PreviewHost} routes straight to the app
	// through the Host header (the reverse proxy must forward it).
	PreviewHost string
	// CloudflareAPIToken authenticates auto-DNS for preview hostnames
	// (scope Zone.DNS:Edit on the preview zone). Auto-DNS is disabled
	// when any of the three Cloudflare settings is empty.
	CloudflareAPIToken string
	// CloudflareZoneID is the zone that owns the preview host.
	CloudflareZoneID string
	// CloudflareTunnelID is the named tunnel every preview CNAME targets
	// ({tunnelID}.cfargotunnel.com).
	CloudflareTunnelID string
	// GitHubAPIURL overrides the GitHub REST API base URL used by prebuilt
	// (artifact) deploys. TEST ONLY — the e2e points it at a fake GitHub;
	// empty means https://api.github.com.
	GitHubAPIURL string
	// BackupDestinations are the named off-box storage targets for database
	// backups, keyed by destination id. Each is registered from its own env
	// block, DEPLOYMATE_BACKUP_DEST_<ID>_*; a service's backup config picks
	// one by id ("default" is the documented fallback).
	BackupDestinations map[string]BackupDestination
}

// BackupDestination is one backup storage target. Type "s3" uploads to any
// S3-compatible bucket (Cloudflare R2 in production); type "local" writes
// under Dir — dev + e2e only, never a backup of record.
type BackupDestination struct {
	ID        string
	Type      string
	Endpoint  string // s3
	AccessKey string // s3
	SecretKey string // s3
	Bucket    string // s3
	Dir       string // local
}

// Load reads configuration from the environment, applying defaults suitable
// for local development.
func Load() (*Config, error) {
	cfg := &Config{
		Addr:               getenv("DEPLOYMATE_ADDR", "127.0.0.1:8080"),
		DataDir:            getenv("DEPLOYMATE_DATA_DIR", "./data"),
		SetupEmail:         os.Getenv("DEPLOYMATE_SETUP_EMAIL"),
		SetupPassword:      os.Getenv("DEPLOYMATE_SETUP_PASSWORD"),
		LEMode:             getenv("DEPLOYMATE_LE_MODE", "staging"),
		RailpackPath:       getenv("DEPLOYMATE_RAILPACK", "railpack"),
		PreviewHost:        getenv("DEPLOYMATE_PREVIEW_HOST", ""),
		CloudflareAPIToken: os.Getenv("DEPLOYMATE_CLOUDFLARE_API_TOKEN"),
		CloudflareZoneID:   os.Getenv("DEPLOYMATE_CLOUDFLARE_ZONE_ID"),
		CloudflareTunnelID: os.Getenv("DEPLOYMATE_CLOUDFLARE_TUNNEL_ID"),
		GitHubAPIURL:       os.Getenv("DEPLOYMATE_GITHUB_API_URL"),
	}
	cfg.KeyPath = filepath.Join(cfg.DataDir, "keys", "root.key")

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "keys"), 0o700); err != nil {
		return nil, fmt.Errorf("create keys dir: %w", err)
	}
	for _, sub := range []string{"builds", "repos", "backups"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create %s dir: %w", sub, err)
		}
	}
	dests, err := parseBackupDestinations(os.Environ())
	if err != nil {
		return nil, err
	}
	cfg.BackupDestinations = dests
	return cfg, nil
}

const backupDestPrefix = "DEPLOYMATE_BACKUP_DEST_"

// parseBackupDestinations reads one env block per destination id:
// DEPLOYMATE_BACKUP_DEST_<ID>_TYPE=s3|local plus the type's fields
// (ENDPOINT/ACCESS_KEY/SECRET_KEY/BUCKET for s3, DIR for local). An id with
// no TYPE is not a destination and is ignored; a malformed block fails the
// whole load — a typo'd destination must never silently fall back to
// "default" at save time. Ids are normalized to lowercase so the config
// field's own "default" fallback matches DEPLOYMATE_BACKUP_DEST_DEFAULT_*.
func parseBackupDestinations(environ []string) (map[string]BackupDestination, error) {
	byID := map[string]BackupDestination{}
	for _, kv := range environ {
		if !strings.HasPrefix(kv, backupDestPrefix) {
			continue
		}
		name, value, _ := strings.Cut(kv, "=")
		rest := strings.TrimPrefix(name, backupDestPrefix)
		id, field, ok := strings.Cut(rest, "_")
		if !ok || id == "" {
			return nil, fmt.Errorf("%s: env var %s must be DEPLOYMATE_BACKUP_DEST_<ID>_<FIELD>", name, name)
		}
		id = strings.ToLower(id)
		d := byID[id]
		switch strings.ToUpper(field) {
		case "TYPE":
			d.Type = strings.ToLower(value)
		case "ENDPOINT":
			d.Endpoint = value
		case "ACCESS_KEY":
			d.AccessKey = value
		case "SECRET_KEY":
			d.SecretKey = value
		case "BUCKET":
			d.Bucket = value
		case "DIR":
			d.Dir = value
		default:
			return nil, fmt.Errorf("%s: unknown field %q", name, field)
		}
		d.ID = id
		byID[id] = d
	}
	out := make(map[string]BackupDestination, len(byID))
	for id, d := range byID {
		switch d.Type {
		case "s3":
			if d.Endpoint == "" || d.AccessKey == "" || d.SecretKey == "" || d.Bucket == "" {
				return nil, fmt.Errorf("backup destination %q: s3 needs ENDPOINT, ACCESS_KEY, SECRET_KEY and BUCKET", id)
			}
		case "local":
			if d.Dir == "" {
				return nil, fmt.Errorf("backup destination %q: local needs DIR", id)
			}
		case "":
			return nil, fmt.Errorf("backup destination %q: missing TYPE (s3 or local)", id)
		default:
			return nil, fmt.Errorf("backup destination %q: unknown TYPE %q (s3 or local)", id, d.Type)
		}
		out[id] = d
	}
	return out, nil
}

// DBPath returns the path to the SQLite database file.
func (c *Config) DBPath() string { return filepath.Join(c.DataDir, "data.db") }

// CloudflareEnabled reports whether auto-DNS is configured: all three
// Cloudflare settings present.
func (c *Config) CloudflareEnabled() bool {
	return c.CloudflareAPIToken != "" && c.CloudflareZoneID != "" && c.CloudflareTunnelID != ""
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
