package config

import "testing"

func TestCloudflareEnabled(t *testing.T) {
	base := Config{
		CloudflareAPIToken:  "tok",
		CloudflareZoneID:    "zone",
		CloudflareTunnelID:  "tun",
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
