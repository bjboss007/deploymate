// Package config loads DeployMate configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	// LEMode selects the Let's Encrypt resolver: "staging" (default) or
	// "production".
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
	}
	cfg.KeyPath = filepath.Join(cfg.DataDir, "keys", "root.key")

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "keys"), 0o700); err != nil {
		return nil, fmt.Errorf("create keys dir: %w", err)
	}
	for _, sub := range []string{"builds", "repos"} {
		if err := os.MkdirAll(filepath.Join(cfg.DataDir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("create %s dir: %w", sub, err)
		}
	}
	return cfg, nil
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
