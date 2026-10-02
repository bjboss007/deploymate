package httpserver

import (
	"fmt"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// runPayload builds a workflow_run payload (real GitHub's shape, spike S1).
type runPayload struct {
	Action, Conclusion, Path, Event, Branch, HeadRepo, Repo string
	ID                                                      int64
	Number                                                  int
}

func goodRun(id int64, number int) runPayload {
	return runPayload{
		Action: "completed", Conclusion: "success", Path: store.DefaultWorkflowPath, Event: "push",
		Branch: "main", HeadRepo: "acme/repo", Repo: "acme/repo", ID: id, Number: number,
	}
}

func (p runPayload) body() string {
	return fmt.Sprintf(`{"action":%q,"workflow_run":{"id":%d,"name":"DeployMate build","path":%q,"event":%q,"status":"completed","conclusion":%q,"head_branch":%q,"head_sha":"abc123def456","run_number":%d,"run_attempt":1,"head_commit":{"message":"ship it"},"head_repository":{"full_name":%q}},"repository":{"full_name":%q}}`,
		p.Action, p.ID, p.Path, p.Event, p.Conclusion, p.Branch, p.Number, p.HeadRepo, p.Repo)
}

// artifactApp adds an app in prebuilt mode on its own source.
func (e *webhookEnv) artifactApp(t *testing.T, slug string) (string, store.App) {
	t.Helper()
	src, app := e.addApp(t, slug, "main")
	if err := e.st.UpdateAppDeployMode(app.ID, store.DeployModeArtifact, "", ""); err != nil {
		t.Fatal(err)
	}
	return src, app
}

func (e *webhookEnv) lastDeployment(t *testing.T, app store.App) store.Deployment {
	t.Helper()
	ds, err := e.st.ListDeployments(app.ID, 1)
	if err != nil || len(ds) == 0 {
		t.Fatalf("no deployments for %s (err %v)", app.Slug, err)
	}
	return ds[0]
}

// TestWorkflowRunQueuesCIDeployment: a successful completed push run of the
// app's workflow on the tracked branch queues a `ci` deployment carrying the
// run identity and commit.
func TestWorkflowRunQueuesCIDeployment(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.artifactApp(t, "api")
	code, body := e.deliver(t, src, "workflow_run", "g1", goodRun(9001, 7).body())
	if code != 200 || body != "queued" {
		t.Fatalf("got %d %q, want 200 queued", code, body)
	}
	d := e.lastDeployment(t, app)
	if d.Kind != "deploy" || d.Status != "queued" || d.Trigger != "ci" || d.CIRun != 9001 || d.CIRunNumber != 7 ||
		d.CommitSHA != "abc123def456" || d.CommitMessage != "ship it" {
		t.Errorf("deployment = %+v", d)
	}
	// workflow_dispatch (a manual run) deploys too.
	r := goodRun(9002, 8)
	r.Event = "workflow_dispatch"
	if _, body := e.deliver(t, src, "workflow_run", "g2", r.body()); body != "queued" {
		t.Errorf("workflow_dispatch run: %q, want queued", body)
	}
}

// TestWorkflowRunGates: each gate refuses with its own reason and queues
// nothing — including the fork-PR poisoning case.
func TestWorkflowRunGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*runPayload)
		want   string
	}{
		{"requested (not completed)", func(p *runPayload) { p.Action = "requested" }, "ignored: workflow run is not completed"},
		{"in progress", func(p *runPayload) { p.Action = "in_progress" }, "ignored: workflow run is not completed"},
		{"failed run", func(p *runPayload) { p.Conclusion = "failure" }, "ignored: workflow run did not succeed"},
		{"cancelled run", func(p *runPayload) { p.Conclusion = "cancelled" }, "ignored: workflow run did not succeed"},
		{"another workflow", func(p *runPayload) { p.Path = ".github/workflows/lint.yml" }, "ignored: a different workflow"},
		{"another branch", func(p *runPayload) { p.Branch = "feature" }, "ignored: not the deploy branch"},
		{"pull request run", func(p *runPayload) { p.Event = "pull_request" }, "ignored: only push and manual runs deploy"},
		{"schedule run", func(p *runPayload) { p.Event = "schedule" }, "ignored: only push and manual runs deploy"},
		// The poisoning case: a fork's PR branch is literally named "main",
		// the run is of our workflow path, success — only the event and the
		// fork gates stop it (each is tested alone below).
		{"fork head repo", func(p *runPayload) { p.HeadRepo = "mallory/repo" }, "ignored: the run is from a fork"},
		{"another repository", func(p *runPayload) { p.Repo, p.HeadRepo = "other/repo", "other/repo" }, "ignored: run belongs to a different repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newWebhookEnv(t)
			src, app := e.artifactApp(t, "api")
			p := goodRun(9001, 1)
			tc.mutate(&p)
			code, body := e.deliver(t, src, "workflow_run", "g-"+tc.name, p.Body())
			if code != 200 || body != tc.want {
				t.Errorf("got %d %q, want %q", code, body, tc.want)
			}
			if n := e.deployments(t, app); n != 0 {
				t.Errorf("queued %d deployments, want 0", n)
			}
		})
	}
}

func (p runPayload) Body() string { return p.body() }

// TestWorkflowRunForkPRNeedsBothGates: with the event gate satisfied (a
// pull_request is refused by event; a fork "push" is refused by head repo).
func TestWorkflowRunForkPRNeedsBothGates(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.artifactApp(t, "api")
	pr := goodRun(1, 1)
	pr.Event, pr.HeadRepo = "pull_request", "mallory/repo"
	if _, body := e.deliver(t, src, "workflow_run", "pr", pr.body()); !strings.HasPrefix(body, "ignored:") {
		t.Errorf("fork PR run: %q", body)
	}
	if n := e.deployments(t, app); n != 0 {
		t.Errorf("a fork PR run queued %d deployments", n)
	}
}

// TestWorkflowRunIdempotencyAndOrdering: one live deployment per run id; a
// duplicate delivery is deduped; an older run than one already deployed is
// stale; a failed deployment can be retried by re-running the same run.
func TestWorkflowRunIdempotencyAndOrdering(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.artifactApp(t, "api")
	if _, body := e.deliver(t, src, "workflow_run", "d1", goodRun(5000, 5).body()); body != "queued" {
		t.Fatalf("first run: %q", body)
	}
	// Same GUID retried → duplicate delivery.
	if _, body := e.deliver(t, src, "workflow_run", "d1", goodRun(5000, 5).body()); body != "duplicate delivery ignored" {
		t.Errorf("same GUID: %q", body)
	}
	// Same run, fresh GUID (a replay) → already handled.
	if _, body := e.deliver(t, src, "workflow_run", "d2", goodRun(5000, 5).body()); body != "ignored: this run was already handled" {
		t.Errorf("replayed run: %q", body)
	}
	// An older run (#4) finishing late must not deploy over #5.
	if _, body := e.deliver(t, src, "workflow_run", "d3", goodRun(4000, 4).body()); body != "ignored: a newer run is already deployed" {
		t.Errorf("stale run: %q", body)
	}
	if n := e.deployments(t, app); n != 1 {
		t.Fatalf("deployments = %d, want 1", n)
	}
	// A newer run deploys.
	if _, body := e.deliver(t, src, "workflow_run", "d4", goodRun(6000, 6).body()); body != "queued" {
		t.Errorf("newer run: %q", body)
	}
	// Retry: the deployment for run 6000 fails; GitHub's "Re-run" re-delivers
	// the SAME run id (new GUID) → it deploys again.
	d := e.lastDeployment(t, app)
	d.Status = "failed"
	if err := e.st.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	if _, body := e.deliver(t, src, "workflow_run", "d5", goodRun(6000, 6).body()); body != "queued" {
		t.Errorf("re-run after a failed deploy: %q, want queued", body)
	}
}

// TestWorkflowRunDispatchBetweenModes: build-mode apps never deploy from CI
// events; prebuilt apps never deploy from pushes; both on one source each
// follow their own mode.
func TestWorkflowRunDispatchBetweenModes(t *testing.T) {
	e := newWebhookEnv(t)
	src, built := e.addApp(t, "built", "main")
	art := mustApp(t, e, "artful")
	if err := e.st.UpdateAppDeployMode(art.ID, store.DeployModeArtifact, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateAppGitSource(art.ID, src); err != nil { // both apps share the source
		t.Fatal(err)
	}

	// A CI run: only the prebuilt app deploys.
	if _, body := e.deliver(t, src, "workflow_run", "c1", goodRun(77, 1).body()); body != "queued" {
		t.Fatalf("workflow_run: %q", body)
	}
	if n := e.deployments(t, built); n != 0 {
		t.Errorf("build-mode app got %d deployments from a CI run, want 0", n)
	}
	if n := e.deployments(t, art); n != 1 {
		t.Errorf("prebuilt app got %d deployments, want 1", n)
	}
	// A push: only the build-mode app deploys.
	if _, body := e.deliver(t, src, "push", "p1", pushMain); body != "queued" {
		t.Fatalf("push: %q", body)
	}
	if n := e.deployments(t, built); n != 1 {
		t.Errorf("build-mode app got %d push deployments, want 1", n)
	}
	if n := e.deployments(t, art); n != 1 {
		t.Errorf("prebuilt app deployed from a push (%d deployments)", n)
	}
}

// TestPushIgnoredWhenOnlyPrebuiltApps: a source whose only app is prebuilt
// answers a push with a clear no-op, not "queued".
func TestPushIgnoredWhenOnlyPrebuiltApps(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.artifactApp(t, "api")
	if _, body := e.deliver(t, src, "push", "p1", pushMain); body != "ignored: this app deploys from CI runs, not pushes" {
		t.Errorf("push: %q", body)
	}
	if n := e.deployments(t, app); n != 0 {
		t.Errorf("a push deployed a prebuilt app (%d)", n)
	}
}

// TestWorkflowRunOnBuildOnlySource: the source's apps are all build-mode →
// the CI event is a clean no-op (nothing in existing setups can change).
func TestWorkflowRunOnBuildOnlySource(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.addApp(t, "plain", "main")
	_, body := e.deliver(t, src, "workflow_run", "g", goodRun(1, 1).body())
	if body != "ignored: no app on this source deploys from CI runs" {
		t.Errorf("got %q", body)
	}
	if n := e.deployments(t, app); n != 0 {
		t.Errorf("deployments = %d", n)
	}
}

func TestWorkflowRunBadPayload(t *testing.T) {
	e := newWebhookEnv(t)
	src, _ := e.artifactApp(t, "api")
	if code, _ := e.deliver(t, src, "workflow_run", "g", `{"action":"completed"}`); code != 400 {
		t.Errorf("payload without a workflow_run = %d, want 400", code)
	}
}

// mustApp creates another app in the same project (no source yet).
func mustApp(t *testing.T, e *webhookEnv, slug string) store.App {
	t.Helper()
	owner, _ := e.st.GetUserByEmail("owner@test.dev")
	proj, err := e.st.GetProjectBySlug(owner.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	app, err := e.st.CreateApp(store.App{ProjectID: proj.ID, Name: slug, Slug: slug, Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	return app
}
