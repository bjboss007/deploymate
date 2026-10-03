package store

import "testing"

// TestPrebuiltDefaults: an app is build-mode with the conventional workflow
// and artifact name unless told otherwise — existing apps (migration 0017's
// column defaults) and new ones behave exactly as before.
func TestPrebuiltDefaults(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if app.DeployMode != DeployModeBuild || app.WorkflowPath != DefaultWorkflowPath || app.ArtifactName != DefaultArtifactName {
		t.Fatalf("create defaults = %q %q %q", app.DeployMode, app.WorkflowPath, app.ArtifactName)
	}
	got, err := st.GetAppByID(app.ID)
	if err != nil || got.DeployMode != DeployModeBuild {
		t.Fatalf("read back = %+v, %v", got, err)
	}
}

func TestUpdateAppDeployMode(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if err := st.UpdateAppDeployMode(app.ID, DeployModeArtifact, ".github/workflows/ci.yml", "my-jar"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetAppBySlug(app.Slug)
	if got.DeployMode != DeployModeArtifact || got.WorkflowPath != ".github/workflows/ci.yml" || got.ArtifactName != "my-jar" {
		t.Errorf("after update = %q %q %q", got.DeployMode, got.WorkflowPath, got.ArtifactName)
	}
	// Empty workflow/artifact fall back to the defaults; build mode keeps them.
	if err := st.UpdateAppDeployMode(app.ID, DeployModeBuild, "", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAppBySlug(app.Slug)
	if got.DeployMode != DeployModeBuild || got.WorkflowPath != DefaultWorkflowPath || got.ArtifactName != DefaultArtifactName {
		t.Errorf("reset = %q %q %q", got.DeployMode, got.WorkflowPath, got.ArtifactName)
	}
	if err := st.UpdateAppDeployMode(app.ID, "nonsense", "", ""); err == nil {
		t.Error("an unknown deploy mode must be rejected")
	}
}

func TestGitSourceAPIToken(t *testing.T) {
	st, _ := newReplicaTestApp(t)
	gs, err := st.CreateGitSource(GitSource{Provider: "github", RepoURL: "https://github.com/o/r", DefaultBranch: "main", APITokenEnc: "enc1"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetGitSource(gs.ID); got.APITokenEnc != "enc1" {
		t.Errorf("token = %q, want enc1", got.APITokenEnc)
	}
	if err := st.SetGitSourceAPIToken(gs.ID, "enc2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetGitSource(gs.ID); got.APITokenEnc != "enc2" {
		t.Errorf("replaced token = %q", got.APITokenEnc)
	}
	_ = st.SetGitSourceAPIToken(gs.ID, "")
	if got, _ := st.GetGitSource(gs.ID); got.APITokenEnc != "" {
		t.Errorf("cleared token = %q", got.APITokenEnc)
	}
}

// TestCIDeploymentFields: the run id/number round-trip through create, get,
// list, and the latest-of-kind query (they all share one column list).
func TestCIDeploymentFields(t *testing.T) {
	st, app := newReplicaTestApp(t)
	d, err := st.CreateDeployment(Deployment{AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "ci", CIRun: 37001586057, CIRunNumber: 4})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetDeployment(d.ID); got.CIRun != 37001586057 || got.CIRunNumber != 4 {
		t.Errorf("get = run %d #%d", got.CIRun, got.CIRunNumber)
	}
	if l, _ := st.ListDeployments(app.ID, 5); len(l) != 1 || l[0].CIRun != 37001586057 {
		t.Errorf("list = %+v", l)
	}
	if l, _ := st.LatestDeploymentOfKind(app.ID, "deploy"); l == nil || l.CIRunNumber != 4 {
		t.Errorf("latest = %+v", l)
	}
	// UpdateDeployment must not clobber the run identity.
	d.Status = "running"
	if err := st.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetDeployment(d.ID); got.CIRun != 37001586057 || got.Status != "running" {
		t.Errorf("after update = %+v", got)
	}
}

// TestCIIdempotencyAndOrdering backs webhook gate 6: one live deployment per
// run id; both it and the run-number ordering ignore FAILED deployments (so a
// failed deploy can be retried by re-running the workflow) but count
// queued/building/running ones.
func TestCIIdempotencyAndOrdering(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if n, err := st.LatestCIRunNumber(app.ID); err != nil || n != 0 {
		t.Fatalf("no CI deployments: latest=%d err=%v", n, err)
	}
	mk := func(run int64, num int, status string) {
		t.Helper()
		if _, err := st.CreateDeployment(Deployment{AppID: app.ID, Kind: "deploy", Status: status, Trigger: "ci", CIRun: run, CIRunNumber: num}); err != nil {
			t.Fatal(err)
		}
	}
	mk(100, 3, "running")
	mk(101, 5, "failed") // failed: must not count
	mk(102, 4, "queued")
	if n, _ := st.LatestCIRunNumber(app.ID); n != 4 {
		t.Errorf("latest = %d, want 4 (running #3 and queued #4 count; failed #5 does not)", n)
	}
	if ok, _ := st.HasCIRun(app.ID, 101); ok {
		t.Error("a FAILED deployment's run id must not count as handled — GitHub's Re-run keeps the id and is how a failed deploy is retried")
	}
	if ok, _ := st.HasCIRun(app.ID, 100); !ok {
		t.Error("a running deployment's run id is handled")
	}
	if ok, _ := st.HasCIRun(app.ID, 102); !ok {
		t.Error("a queued deployment's run id is handled")
	}
	if ok, _ := st.HasCIRun(app.ID, 999); ok {
		t.Error("an unknown run must not be seen")
	}
	// Non-CI deployments (ci_run = 0) never affect ordering.
	_, _ = st.CreateDeployment(Deployment{AppID: app.ID, Kind: "manual", Status: "running", CIRunNumber: 99})
	if n, _ := st.LatestCIRunNumber(app.ID); n != 4 {
		t.Errorf("latest = %d after a non-CI deployment, want 4", n)
	}
}

func TestAppearanceColumns(t *testing.T) {
	st, app := newReplicaTestApp(t)
	if app.Logo != "" || app.Accent != "" || app.Stack != "" {
		t.Fatalf("defaults must be empty (automatic): %+v", app)
	}
	if err := st.UpdateAppAppearance(app.ID, "react", "pink"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAppStack(app.ID, "spring"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAppBySlug(app.Slug)
	if err != nil || got.Logo != "react" || got.Accent != "pink" || got.Stack != "spring" {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	byID, _ := st.GetAppByID(app.ID)
	if byID.Logo != "react" {
		t.Errorf("GetAppByID lost the logo: %+v", byID)
	}
}

func TestAppServiceExclusions(t *testing.T) {
	st, app := newReplicaTestApp(t)
	sv, err := st.CreateService(Service{ProjectID: app.ProjectID, Type: "redis", Name: "r", Slug: "r", Image: "redis:7"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListAppServiceExclusions(app.ID); len(got) != 0 {
		t.Fatalf("default exclusions = %v", got)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := st.SetAppServiceExcluded(app.ID, sv.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := st.ListAppServiceExclusions(app.ID); !got[sv.ID] || len(got) != 1 {
		t.Fatalf("after exclude = %v", got)
	}
	if err := st.SetAppServiceExcluded(app.ID, sv.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListAppServiceExclusions(app.ID); len(got) != 0 {
		t.Fatalf("after include = %v", got)
	}
	// Deleting the service clears its exclusions (FK cascade).
	_ = st.SetAppServiceExcluded(app.ID, sv.ID, true)
	if err := st.DeleteService(sv.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListAppServiceExclusions(app.ID); len(got) != 0 {
		t.Fatalf("exclusion survived its service: %v", got)
	}
}
