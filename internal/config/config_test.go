package config

import (
	"reflect"
	"testing"
)

func TestCloudflareEnabled(t *testing.T) {
	base := Config{
		CloudflareAPIToken: "tok",
		CloudflareZoneID:   "zone",
		CloudflareTunnelID: "tun",
	}
	cases := []struct {
		name   string
		mutate func(*Config)
		want   bool
	}{
		{"all set", func(*Config) {}, true},
		{"no token", func(c *Config) { c.CloudflareAPIToken = "" }, false},
		{"no zone", func(c *Config) { c.CloudflareZoneID = "" }, false},
		{"no tunnel", func(c *Config) { c.CloudflareTunnelID = "" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			if got := c.CloudflareEnabled(); got != tc.want {
				t.Errorf("CloudflareEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLoadCloudflareEnv(t *testing.T) {
	t.Setenv("DEPLOYMATE_CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("DEPLOYMATE_CLOUDFLARE_ZONE_ID", "zone")
	t.Setenv("DEPLOYMATE_CLOUDFLARE_TUNNEL_ID", "tun")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CloudflareAPIToken != "tok" || cfg.CloudflareZoneID != "zone" || cfg.CloudflareTunnelID != "tun" {
		t.Errorf("cloudflare config not loaded: %+v", cfg)
	}
	if !cfg.CloudflareEnabled() {
		t.Error("CloudflareEnabled() = false, want true")
	}
}

func TestLoadBackupDestinations(t *testing.T) {
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEFAULT_TYPE", "s3")
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEFAULT_ENDPOINT", "https://acct.r2.cloudflarestorage.com")
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEFAULT_ACCESS_KEY", "ak")
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEFAULT_SECRET_KEY", "sk")
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEFAULT_BUCKET", "dms")
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEV_TYPE", "local")
	t.Setenv("DEPLOYMATE_BACKUP_DEST_DEV_DIR", "/tmp/dm-backups")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]BackupDestination{
		"default": {ID: "default", Type: "s3", Endpoint: "https://acct.r2.cloudflarestorage.com", AccessKey: "ak", SecretKey: "sk", Bucket: "dms"},
		"dev":     {ID: "dev", Type: "local", Dir: "/tmp/dm-backups"},
	}
	if !reflect.DeepEqual(cfg.BackupDestinations, want) {
		t.Errorf("BackupDestinations = %+v, want %+v", cfg.BackupDestinations, want)
	}
}

// TestLoadBackupDestinationsRejects invalid env blocks fail the whole load —
// a typo'd destination must never silently fall back to "default".
func TestLoadBackupDestinationsRejects(t *testing.T) {
	cases := []struct {
		name  string
		setup func()
	}{
		{"s3 missing bucket", func() {
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_TYPE", "s3")
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_ENDPOINT", "e")
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_ACCESS_KEY", "k")
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_SECRET_KEY", "s")
		}},
		{"local missing dir", func() { t.Setenv("DEPLOYMATE_BACKUP_DEST_A_TYPE", "local") }},
		{"unknown type", func() { t.Setenv("DEPLOYMATE_BACKUP_DEST_A_TYPE", "ftp") }},
		{"unknown field", func() {
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_TYPE", "local")
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_DIR", "/tmp/x")
			t.Setenv("DEPLOYMATE_BACKUP_DEST_A_PASSWORD", "p")
		}},
		{"id without field", func() { t.Setenv("DEPLOYMATE_BACKUP_DEST_A", "x") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			if _, err := Load(); err == nil {
				t.Error("Load() = nil error, want rejection")
			}
		})
	}
}

func TestParseBackupDestinationsIgnoresOthers(t *testing.T) {
	dests, err := parseBackupDestinations([]string{"DEPLOYMATE_ADDR=:1", "PATH=/bin", "DEPLOYMATE_BACKUP_DEST_OTHER_TYPE=local", "DEPLOYMATE_BACKUP_DEST_OTHER_DIR=/tmp/x"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(dests) != 1 || dests["other"].Type != "local" {
		t.Errorf("dests = %+v, want only other/local", dests)
	}
}
