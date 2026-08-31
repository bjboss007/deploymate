package gitpkg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// buildMirrorRemote creates a work repo with three commits and a bare
// remote. first = "one" in a.txt; second = "zero\n" in a.txt (removes 1
// line) plus b.txt (adds 1); third = c.txt (adds 1) — the head. Returns the
// remote path and the three shas.
func buildMirrorRemote(t *testing.T) (remote, first, second, third string) {
	t.Helper()
	work := t.TempDir()
	runGitT(t, work, "init", "-b", "main")
	runGitT(t, work, "config", "user.email", "t@test.dev")
	runGitT(t, work, "config", "user.name", "T")

	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, work, "add", ".")
	runGitT(t, work, "commit", "-m", "first commit")
	first = revParseT(t, work)

	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("zero\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, work, "add", ".")
	runGitT(t, work, "commit", "-m", "second commit")
	second = revParseT(t, work)

	if err := os.WriteFile(filepath.Join(work, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, work, "add", ".")
	runGitT(t, work, "commit", "-m", "third commit")
	third = revParseT(t, work)

	remote = filepath.Join(t.TempDir(), "remote.git")
	runGitT(t, work, "clone", "--bare", work, remote)
	return remote, first, second, third
}

func TestMirrorRange(t *testing.T) {
	remote, first, _, third := buildMirrorRemote(t)
	dir := filepath.Join(t.TempDir(), "mirror")
	ctx := context.Background()

	if err := MirrorSync(ctx, remote, "main", "", dir); err != nil {
		t.Fatalf("sync: %v", err)
	}

	rg, err := MirrorRange(ctx, dir, "main", first)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if rg.Head != third {
		t.Fatalf("head = %s, want %s", rg.Head, third)
	}
	if len(rg.Commits) != 2 {
		t.Fatalf("commits = %d, want 2", len(rg.Commits))
	}
	if rg.Commits[0].Subject != "third commit" || rg.Commits[0].Author != "T" {
		t.Fatalf("newest commit = %+v", rg.Commits[0])
	}
	if rg.Files != 3 || rg.Added != 3 || rg.Deleted != 1 {
		t.Fatalf("diff = %d files, +%d/-%d; want 3 files, +3/-1", rg.Files, rg.Added, rg.Deleted)
	}
	if got := rg.SummaryLine(); got != "2 commits, 3 files, +3/-1" {
		t.Fatalf("summary = %q", got)
	}

	// Deployed SHA == head: nothing to deploy.
	rg, err = MirrorRange(ctx, dir, "main", third)
	if err != nil {
		t.Fatalf("range (equal): %v", err)
	}
	if len(rg.Commits) != 0 || rg.Files != 0 {
		t.Fatalf("equal range not empty: %+v", rg)
	}

	// First deploy (no deployed SHA): just the head commit, no diff counts.
	rg, err = MirrorRange(ctx, dir, "main", "")
	if err != nil {
		t.Fatalf("range (first): %v", err)
	}
	if len(rg.Commits) != 1 || rg.Commits[0].Hash != third {
		t.Fatalf("first-deploy range = %+v, want just head", rg)
	}
	if rg.Files != 0 {
		t.Fatalf("first-deploy files = %d, want 0", rg.Files)
	}
}

// TestMirrorSyncIncremental proves a second sync fetches new commits instead
// of re-cloning.
func TestMirrorSyncIncremental(t *testing.T) {
	remote, _, _, third := buildMirrorRemote(t)
	dir := filepath.Join(t.TempDir(), "mirror")
	ctx := context.Background()
	if err := MirrorSync(ctx, remote, "main", "", dir); err != nil {
		t.Fatalf("sync 1: %v", err)
	}

	// A fourth commit on the remote.
	work := t.TempDir()
	runGitT(t, work, "clone", remote, "clone")
	cloneDir := filepath.Join(work, "clone")
	runGitT(t, cloneDir, "config", "user.email", "t@test.dev")
	runGitT(t, cloneDir, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(cloneDir, "d.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, cloneDir, "add", ".")
	runGitT(t, cloneDir, "commit", "-m", "fourth commit")
	fourth := revParseT(t, cloneDir)
	runGitT(t, cloneDir, "push", "origin", "main")

	if err := MirrorSync(ctx, remote, "main", "", dir); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	rg, err := MirrorRange(ctx, dir, "main", third)
	if err != nil {
		t.Fatalf("range: %v", err)
	}
	if rg.Head != fourth || len(rg.Commits) != 1 || rg.Commits[0].Subject != "fourth commit" {
		t.Fatalf("incremental range = %+v, want one 'fourth commit'", rg)
	}
}

// TestMirrorEnsureSHAProbesLocalAndFailsGone proves ErrCommitGone for a SHA
// the remote cannot serve (never advertised to the mirror).
func TestMirrorEnsureSHAProbesLocalAndFailsGone(t *testing.T) {
	remote, first, _, _ := buildMirrorRemote(t)
	dir := filepath.Join(t.TempDir(), "mirror")
	ctx := context.Background()
	if err := MirrorSync(ctx, remote, "main", "", dir); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// The commit exists in the mirror already — no remote call needed.
	if err := MirrorEnsureSHA(ctx, dir, remote, "", first); err != nil {
		t.Fatalf("ensure existing: %v", err)
	}
	// A bogus SHA must surface as ErrCommitGone.
	if err := MirrorEnsureSHA(ctx, dir, remote, "", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); !errors.Is(err, ErrCommitGone) {
		t.Fatalf("ensure bogus: err = %v, want ErrCommitGone", err)
	}
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func revParseT(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return string(out)[:40]
}
