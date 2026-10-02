package githubci

import (
	"fmt"
	"strconv"
	"strings"
)

// Build tools the generated workflow knows.
const (
	ToolGradle = "gradle"
	ToolMaven  = "maven"
)

// WorkflowOpts fills the generated GitHub Actions workflow
// (docs/specs/prebuilt-deploys.md, "Generated workflow").
type WorkflowOpts struct {
	Branch       string // the app's tracked branch
	JavaMajor    string // "21"
	ArtifactName string // must equal the app's artifact name
	Tool         string // ToolGradle (default) or ToolMaven
}

// Workflow renders a workflow file that builds the app's JAR on a GitHub
// runner and uploads exactly one file, out/app.jar, as a one-day artifact —
// the shape DeployMate's prebuilt mode expects. The `cp` step is what makes
// multi-module builds safe: the artifact always holds a single JAR, so
// DeployMate never has to guess which one is the application.
func Workflow(o WorkflowOpts) string {
	if o.JavaMajor == "" {
		o.JavaMajor = "21"
	}
	if o.ArtifactName == "" {
		o.ArtifactName = "deploymate-app"
	}
	build, cache := "./gradlew bootJar --no-daemon", "gradle"
	pick := `cp "$(ls build/libs/*.jar | grep -v -- -plain | head -n1)" out/app.jar`
	if o.Tool == ToolMaven {
		build, cache = "./mvnw -B -DskipTests package", "maven"
		pick = `cp "$(ls target/*.jar | grep -v -- -plain | head -n1)" out/app.jar`
	}
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("name: DeployMate build")
	w("on:")
	w("  push:")
	w("    branches: [%s]", strconv.Quote(o.Branch))
	w("  workflow_dispatch:")
	w("permissions:")
	w("  contents: read")
	w("jobs:")
	w("  build:")
	w("    runs-on: ubuntu-latest")
	w("    steps:")
	w("      - uses: actions/checkout@v4")
	w("      - uses: actions/setup-java@v4")
	w("        with: { distribution: temurin, java-version: %s, cache: %s }", strconv.Quote(o.JavaMajor), cache)
	w("      - run: %s", build)
	w("      - run: mkdir out && %s", pick)
	w("      - uses: actions/upload-artifact@v4")
	w("        with:")
	w("          name: %s", strconv.Quote(o.ArtifactName))
	w("          path: out/app.jar")
	w("          retention-days: 1")
	w("          if-no-files-found: error")
	return b.String()
}
