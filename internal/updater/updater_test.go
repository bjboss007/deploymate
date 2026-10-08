package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeArchive(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(dir, "deploymate_linux_amd64.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	a := makeArchive(t, dir, map[string]string{"./deploymate": "x"})
	sum, _ := SHA256File(a)
	sums := filepath.Join(dir, "checksums.txt")
	name := filepath.Base(a)

	os.WriteFile(sums, []byte(sum+"  "+name+"\n"), 0o644)
	if err := VerifyChecksum(a, sums); err != nil {
		t.Errorf("good checksum refused: %v", err)
	}
	os.WriteFile(sums, []byte(strings.Repeat("0", 64)+"  "+name+"\n"), 0o644)
	if err := VerifyChecksum(a, sums); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("tampered archive accepted: %v", err)
	}
	os.WriteFile(sums, []byte(sum+"  other.tar.gz\n"), 0o644)
	if err := VerifyChecksum(a, sums); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Errorf("unlisted archive accepted: %v", err)
	}
}

func TestExtractOnlyNamedEntries(t *testing.T) {
	dir := t.TempDir()
	a := makeArchive(t, dir, map[string]string{
		"./deploymate":                "bin",
		"./deploy/deploymate.service": "unit",
		"../../evil":                  "no",
	})
	out := t.TempDir()
	if err := Extract(a, out, map[string]string{"deploymate": "deploymate", "deploy/deploymate.service": "deploymate.service"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "deploymate.service")); string(b) != "unit" {
		t.Errorf("unit = %q", b)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 2 {
		t.Errorf("extracted %d files, want 2", len(entries))
	}
	if err := Extract(a, t.TempDir(), map[string]string{"missing": "missing"}); err == nil {
		t.Error("a missing entry must be an error")
	}
}

type fakeEnv struct {
	in       *Installer
	dir      string
	calls    []string
	healthOK bool
}

func newFake(t *testing.T, healthy bool) (*fakeEnv, string) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	os.MkdirAll(data, 0o755)
	os.WriteFile(filepath.Join(data, "data.db"), []byte("OLD-DB"), 0o600)
	bin := filepath.Join(root, "deploymate")
	os.WriteFile(bin, []byte("OLD-BIN"), 0o755)
	unit := filepath.Join(root, "deploymate.service")
	os.WriteFile(unit, []byte("OLD-UNIT"), 0o644)
	pkg := filepath.Join(root, "pkg")
	os.MkdirAll(pkg, 0o755)
	os.WriteFile(filepath.Join(pkg, "deploymate"), []byte("NEW-BIN"), 0o755)
	os.WriteFile(filepath.Join(pkg, "deploymate.service"), []byte("ExecStart=x\nEnvironment=DEPLOYMATE_DATA_DIR=/var/lib/deploymate\n"), 0o644)

	f := &fakeEnv{dir: root, healthOK: healthy}
	f.in = &Installer{
		Binary: bin, UnitPath: unit, DataDir: data, Out: &bytes.Buffer{}, HealthTimeout: 2 * time.Second,
		Systemctl: func(_ context.Context, a ...string) error {
			f.calls = append(f.calls, strings.Join(a, " "))
			if len(a) > 0 && a[0] == "start" && f.healthOK {
				// the new service "writes" to the db like a migration would
				os.WriteFile(filepath.Join(data, "data.db"), []byte("MIGRATED-DB"), 0o600)
			}
			return nil
		},
		Healthy: func(context.Context) error {
			if f.healthOK {
				return nil
			}
			return errors.New("connection refused")
		},
		SmokeTest: func(_ context.Context, b string) (string, error) { return "deploymate v9", nil },
	}
	return f, pkg
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplySuccess(t *testing.T) {
	f, pkg := newFake(t, true)
	if err := f.in.Apply(context.Background(), pkg); err != nil {
		t.Fatal(err)
	}
	if read(t, f.in.Binary) != "NEW-BIN" || read(t, f.in.Binary+".prev") != "OLD-BIN" {
		t.Error("binary not swapped / previous not kept")
	}
	if read(t, filepath.Join(f.in.DataDir, "data.db.pre-update")) != "OLD-DB" {
		t.Error("database not backed up before the swap")
	}
	if !strings.Contains(read(t, f.in.UnitPath), "DEPLOYMATE_DATA_DIR="+f.in.DataDir) {
		t.Error("unit did not keep this install's data dir")
	}
	if got := strings.Join(f.calls, ","); got != "stop deploymate,daemon-reload,start deploymate" {
		t.Errorf("systemctl calls = %s", got)
	}
}

func TestApplyRollsBackWhenUnhealthy(t *testing.T) {
	f, pkg := newFake(t, false)
	f.in.HealthTimeout = 1500 * time.Millisecond
	err := f.in.Apply(context.Background(), pkg)
	if err == nil {
		t.Fatal("an unhealthy new version must be an error")
	}
	if read(t, f.in.Binary) != "OLD-BIN" {
		t.Errorf("binary = %q, want the old one back", read(t, f.in.Binary))
	}
	if read(t, filepath.Join(f.in.DataDir, "data.db")) != "OLD-DB" {
		t.Error("database not restored")
	}
	if read(t, f.in.UnitPath) != "OLD-UNIT" {
		t.Error("unit not restored")
	}
	last := f.calls[len(f.calls)-1]
	if last != "start deploymate" {
		t.Errorf("old version not restarted; last call %q", last)
	}
}

func TestApplyRefusesABinaryThatDoesNotRun(t *testing.T) {
	f, pkg := newFake(t, true)
	f.in.SmokeTest = func(context.Context, string) (string, error) { return "", fmt.Errorf("exec format error") }
	if err := f.in.Apply(context.Background(), pkg); err == nil {
		t.Fatal("expected an error")
	}
	if len(f.calls) != 0 {
		t.Errorf("the service was touched (%v) before the new binary was proven", f.calls)
	}
	if read(t, f.in.Binary) != "OLD-BIN" {
		t.Error("binary changed")
	}
}

func TestResolveBase(t *testing.T) {
	for _, c := range []struct{ ver, over, want string }{
		{"latest", "", "https://github.com/o/r/releases/latest/download"},
		{"v1.2.3", "", "https://github.com/o/r/releases/download/v1.2.3"},
		{"v1.2.3", "http://mirror/x/", "http://mirror/x"},
	} {
		if got := ResolveBase("o/r", c.ver, c.over); got != c.want {
			t.Errorf("ResolveBase(%q,%q) = %q", c.ver, c.over, got)
		}
	}
}
