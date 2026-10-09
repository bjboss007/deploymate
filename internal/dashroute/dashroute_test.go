package dashroute

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderDashboardOnly(t *testing.T) {
	b, err := Render(Opts{Host: "dm.example.com", ListenAddr: "127.0.0.1:8080", Resolver: "letsencrypt"})
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("not valid YAML: %v\n%s", err, b)
	}
	s := string(b)
	for _, want := range []string{"Host(`dm.example.com`)", "http://127.0.0.1:8080", "certResolver: letsencrypt", "websecure"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "deploymate-previews") {
		t.Error("no preview router without a preview host")
	}
}

func TestRenderPreviewCatchAllHasLowestPriority(t *testing.T) {
	b, err := Render(Opts{Host: "dm.example.com", PreviewHost: "dm.example.com", ListenAddr: "0.0.0.0:8080"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "priority: 1") {
		t.Errorf("the catch-all must carry priority 1 so app routers win:\n%s", s)
	}
	if !strings.Contains(s, "http://127.0.0.1:8080") {
		t.Errorf("a wildcard bind must be reached on loopback:\n%s", s)
	}
	if strings.Contains(s, "certResolver") {
		t.Errorf("no resolver was asked for:\n%s", s)
	}
	var f struct {
		HTTP struct {
			Routers map[string]struct {
				Rule string `yaml:"rule"`
			} `yaml:"routers"`
		} `yaml:"http"`
	}
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if got := f.HTTP.Routers["deploymate-previews"].Rule; got != "HostRegexp(`^[a-z0-9-]+\\.dm\\.example\\.com$`)" {
		t.Errorf("preview rule = %q", got)
	}
}

func TestHostIsValidatedBeforeItReachesARule(t *testing.T) {
	for _, bad := range []string{"", "DM.example.com", "dm.example.com`) || Host(`x", "a b.com", "localhost", "-a.com", "a..com", "dm.example.com/x", "dm.example.com\n"} {
		if ValidHost(bad) {
			t.Errorf("%q accepted", bad)
		}
		if _, err := Render(Opts{Host: bad, ListenAddr: "127.0.0.1:8080"}); err == nil {
			t.Errorf("Render accepted %q", bad)
		}
	}
	if _, err := Render(Opts{Host: "dm.example.com", PreviewHost: "x`) || Host(`y", ListenAddr: "127.0.0.1:8080"}); err == nil {
		t.Error("a bad preview host was accepted")
	}
	for _, good := range []string{"dm.example.com", "a.b.c.example.org", "xn--bcher-kva.example"} {
		if !ValidHost(good) {
			t.Errorf("%q refused", good)
		}
	}
}

func TestWriteAndRemove(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "traefik-dynamic")
	o := Opts{Host: "dm.example.com", ListenAddr: "127.0.0.1:8080"}
	if err := Write(dir, o); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, FileName)
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("file: %v %v", err, fi)
	}
	other := filepath.Join(dir, "mine.yml")
	os.WriteFile(other, []byte("x"), 0o644)
	if err := Write(dir, Opts{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("an empty host must remove the route")
	}
	if _, err := os.Stat(other); err != nil {
		t.Error("Write touched a file that is not its own")
	}
	if err := Write(dir, Opts{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Errorf("removing nothing must not fail: %v", err)
	}
}
