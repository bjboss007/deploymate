package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildErrorGradleOOM replays the tail of a real failed deploy
// (2026-09-30: a Gradle daemon with a 2 GiB heap in a 3.8 GiB Docker
// Desktop VM): the error must say "out of memory", quote the evidence, and
// suggest a fix — not just "exit status 1".
func TestBuildErrorGradleOOM(t *testing.T) {
	raw, err := os.ReadFile("testdata/gradle_oom.log")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	got := buildError("railpack build failed", lines, errors.New("exit status 1"))
	if !errors.Is(got, ErrOutOfMemory) {
		t.Fatalf("want ErrOutOfMemory, got %v", got)
	}
	for _, want := range []string{"cannot allocate memory", "org.gradle.jvmargs", "Give Docker more memory", "exit status 1"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, got)
		}
	}
}

// TestBuildErrorQuotesBuilderError: a non-memory failure quotes the
// builder's own last error line.
func TestBuildErrorQuotesBuilderError(t *testing.T) {
	lines := []string{
		"#9 12.3 npm ERR! missing script: build",
		`#9 ERROR: process "/bin/sh -c npm run build" did not complete successfully: exit code: 1`,
		"------",
		" > npm run build:",
		"------",
		` ERRO failed to solve: process "/bin/sh -c npm run build" did not complete successfully: exit code: 1`,
	}
	got := buildError("railpack build failed", lines, errors.New("exit status 1"))
	if errors.Is(got, ErrOutOfMemory) {
		t.Fatalf("a script failure is not OOM: %v", got)
	}
	want := `railpack build failed: ERRO failed to solve: process "/bin/sh -c npm run build" did not complete successfully: exit code: 1: exit status 1`
	if got.Error() != want {
		t.Errorf("got  %q\nwant %q", got.Error(), want)
	}
}

func TestBuildErrorNoClues(t *testing.T) {
	got := buildError("build failed", []string{"step 1", "step 2"}, errors.New("exit status 1"))
	if got.Error() != "build failed: exit status 1" {
		t.Errorf("got %q", got.Error())
	}
	long := "error: " + strings.Repeat("x", 1000)
	if got := buildError("build failed", []string{long}, errors.New("x")); len(got.Error()) > maxCauseLen+50 {
		t.Errorf("cause must be clipped, got %d chars", len(got.Error()))
	}
}

// TestBuildRailpackSurfacesCause wires the diagnosis through the real
// process path: a stand-in "railpack" that prints an OOM and exits 1.
func TestBuildRailpackSurfacesCause(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "railpack")
	script := "#!/bin/sh\necho 'building...'\necho ' ERRO failed to solve: ResourceExhausted: cannot allocate memory' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var logged []string
	err := BuildRailpack(context.Background(), fake, t.TempDir(), "img:1", RuntimeSpec{Key: "node"},
		func(l string) { logged = append(logged, l) })
	if !errors.Is(err, ErrOutOfMemory) || !strings.Contains(err.Error(), "cannot allocate memory") {
		t.Fatalf("err = %v, want ErrOutOfMemory quoting the evidence", err)
	}
	if len(logged) != 2 {
		t.Errorf("every output line must still reach the build log, got %v", logged)
	}
}
