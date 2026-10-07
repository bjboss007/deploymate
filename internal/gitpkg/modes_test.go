package gitpkg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

// A clone made under the service's 0077 umask must come out readable by other
// users (the user a container runs as), with executables still executable.
func TestCloneIsReadableUnderTheServicesUmask(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	src := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(src, "init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(src, "site", "assets"), 0o755)
	os.WriteFile(filepath.Join(src, "site", "index.html"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(src, "site", "assets", "app.css"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o755)
	run(src, "add", ".")
	run(src, "commit", "-q", "-m", "init")

	old := syscall.Umask(0o077) // what the systemd unit sets
	defer syscall.Umask(old)
	dest := filepath.Join(t.TempDir(), "checkout")
	if err := Clone(context.Background(), "file://"+src, "main", "", "", dest); err != nil {
		t.Fatal(err)
	}
	want := map[string]os.FileMode{
		dest:                                             0o755,
		filepath.Join(dest, "site"):                      0o755,
		filepath.Join(dest, "site", "assets"):            0o755,
		filepath.Join(dest, "site", "index.html"):        0o644,
		filepath.Join(dest, "site", "assets", "app.css"): 0o644,
		filepath.Join(dest, "run.sh"):                    0o755,
	}
	for p, w := range want {
		if got := perm(t, p); got != w {
			t.Errorf("%s = %o, want %o", p, got, w)
		}
	}
}

func TestNormalizeModesLeavesGitAndSymlinksAlone(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o700)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o600)
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte("s"), 0o600)
	os.Symlink(outside, filepath.Join(root, "link"))
	if err := normalizeModes(root); err != nil {
		t.Fatal(err)
	}
	if got := perm(t, filepath.Join(root, ".git", "config")); got != 0o600 {
		t.Errorf(".git was touched: %o", got)
	}
	if got := perm(t, filepath.Join(root, "a.txt")); got != 0o644 {
		t.Errorf("a.txt = %o", got)
	}
	if got := perm(t, outside); got != 0o600 {
		t.Errorf("a symlink target outside the checkout was chmodded: %o", got)
	}
}
