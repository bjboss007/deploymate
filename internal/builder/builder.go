// Package builder turns a git checkout into a runnable image.
//
// Detection is Dockerfile-first: if the app's root directory contains a
// Dockerfile, build it with buildx (BuildKit) and load the result into the
// local daemon. Nixpacks support is the planned fallback for repos without
// a Dockerfile.
package builder

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// DetectBuildType returns "dockerfile" if the checkout has a Dockerfile in
// the app root, otherwise the empty string.
func DetectBuildType(checkoutDir, rootDir string) string {
	root := filepath.Join(checkoutDir, filepath.Clean("/"+rootDir))
	if _, err := os.Stat(filepath.Join(root, "Dockerfile")); err == nil {
		return "dockerfile"
	}
	// Case-insensitive fallback matches buildx behavior for names like
	// `dockerfile`.
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Base(e.Name()) == "Dockerfile" {
			return "dockerfile"
		}
	}
	return ""
}

// Build runs `docker buildx build` for the app root and loads the image into
// the local daemon. Every output line is passed to log — the caller fans it
// out to the database and the dashboard.
func Build(ctx context.Context, checkoutDir, rootDir, imageTag string, log func(line string)) error {
	buildCtx := filepath.Join(checkoutDir, filepath.Clean("/"+rootDir)) // never leaves the checkout, whatever rootDir holds
	// The buildx CLI streams BuildKit progress; --load makes the result
	// available to the local daemon immediately.
	cmd := exec.CommandContext(ctx, "docker", "buildx", "build",
		"--progress=plain", "--load", "-t", imageTag, buildCtx,
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("build stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("build stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start build: %w", err)
	}

	// The pipes hit EOF when the process exits, so the waitgroup completes
	// exactly once buildx finishes (or ctx cancels it).
	tail := newOutputTail(log)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scanLines(stdout, tail.Line) }()
	go func() { defer wg.Done(); scanLines(stderr, tail.Line) }()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		// Say WHY (out of memory, or the builder's own error line) instead
		// of a bare "exit status 1".
		return buildError("build failed", tail.Lines(), err)
	}
	return nil
}

// scanLines streams r line by line until EOF.
func scanLines(r io.Reader, line func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line(sc.Text())
	}
}
