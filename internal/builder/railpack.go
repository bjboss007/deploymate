package builder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// BuildKitHost names the BuildKit daemon container Railpack builds with.
// It runs as a `moby/buildkit` container named dm-buildkit (started by
// bootstrap.sh); the docker-container scheme connects by docker exec.
const BuildKitHost = "docker-container://dm-buildkit"

// BuildRailpack builds a checkout with the Railpack CLI and a selected
// runtime. When a version is pinned, a .mise.toml is written into the app
// dir first (unless the repo has its own — the repo wins), which is the
// uniform version-pinning mechanism across all providers.
//
// Railpack pipes the finished image into `docker load`, so after a
// successful build the image is available to the local daemon under
// imageTag — exactly like our buildx Dockerfile path.
func BuildRailpack(ctx context.Context, railpackPath, appDir, imageTag string, spec RuntimeSpec, log func(line string)) error {
	rt, ok := RuntimeByKey(spec.Key)
	if !ok {
		return fmt.Errorf("unknown runtime %q", spec.Key)
	}
	if spec.Version != "" && rt.MiseName != "" {
		misePath := filepath.Join(appDir, ".mise.toml")
		if _, err := os.Stat(misePath); os.IsNotExist(err) {
			content := fmt.Sprintf("[tools]\n%s = %q\n", rt.MiseName, spec.Version)
			if err := os.WriteFile(misePath, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write .mise.toml: %w", err)
			}
			log("pinned runtime via .mise.toml: " + rt.MiseName + " = " + spec.Version)
		}
	}
	// Java: the old Apache mvnw wrapper (pre-maven-wrapper 3.2) demands
	// JAVA_HOME, which Railpack does not export; its managed `mvn` resolves
	// java via mise PATH shims instead. The checkout is throwaway, so drop
	// the wrapper and let the platform's managed Maven build.
	if spec.Key == "java" {
		for _, p := range []string{"mvnw", "mvnw.cmd", ".mvn"} {
			if err := os.RemoveAll(filepath.Join(appDir, p)); err != nil {
				return fmt.Errorf("remove %s: %w", p, err)
			}
		}
		if _, err := os.Stat(filepath.Join(appDir, "mvnw")); os.IsNotExist(err) {
			log("using managed Maven (repo's maven wrapper removed for the build)")
		}
	}

	cmd := exec.CommandContext(ctx, railpackPath, "build", appDir,
		"--name", imageTag,
		"--progress", "plain",
	)
	cmd.Env = append(os.Environ(), "BUILDKIT_HOST="+BuildKitHost)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("railpack stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("railpack stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start railpack (is it installed?): %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scanLines(stdout, log) }()
	go func() { defer wg.Done(); scanLines(stderr, log) }()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("railpack build failed: %w", err)
	}
	return nil
}
