package githubci

import (
	"strings"
	"testing"
)

func TestWorkflowGradle(t *testing.T) {
	got := Workflow(WorkflowOpts{Branch: "development", JavaMajor: "17", ArtifactName: "my-app"})
	for _, want := range []string{
		`branches: ["development"]`,
		"workflow_dispatch:",
		`java-version: "17"`,
		"cache: gradle",
		"./gradlew bootJar --no-daemon",
		"cp \"$(ls build/libs/*.jar | grep -v -- -plain | head -n1)\" out/app.jar",
		`name: "my-app"`,
		"retention-days: 1",
		"if-no-files-found: error",
		"contents: read",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("workflow lacks %q:\n%s", want, got)
		}
	}
}

func TestWorkflowMavenAndDefaults(t *testing.T) {
	got := Workflow(WorkflowOpts{Branch: "main", Tool: ToolMaven})
	for _, want := range []string{"cache: maven", "./mvnw -B -DskipTests package", "target/*.jar", `java-version: "21"`, `name: "deploymate-app"`} {
		if !strings.Contains(got, want) {
			t.Errorf("maven workflow lacks %q", want)
		}
	}
	if strings.Contains(got, "gradle") {
		t.Errorf("maven workflow mentions gradle:\n%s", got)
	}
}

// A hostile branch name can only ever land inside a quoted YAML string.
func TestWorkflowQuotesBranch(t *testing.T) {
	got := Workflow(WorkflowOpts{Branch: `x"] }\n  evil: [`})
	if !strings.Contains(got, `branches: ["x\"] }\\n  evil: ["]`) {
		t.Errorf("branch not safely quoted:\n%s", got)
	}
}
