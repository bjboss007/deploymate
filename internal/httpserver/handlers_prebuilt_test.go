package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
)

const testToken = "github_pat_TESTTOKEN0123456789"

// prebuiltEnv is a Server with one session, one GitHub-linked app on
// acme/repo (branch main), and a fake GitHub API it points at.
type prebuiltEnv struct {
	s    *Server
	st   *store.Store
	app  store.App
	gs   store.GitSource
	runs string // JSON array served as workflow_runs
	repo []string
	hits map[string]int
	auth string // last Authorization header the fake saw
}

func newPrebuiltEnv(t *testing.T) *prebuiltEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	owner, _ := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	proj, _ := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	app, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Api", Slug: "api", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	e := &prebuiltEnv{st: st, app: app, hits: map[string]int{}}
	e.s = &Server{store: st, encKey: [32]byte{9}, deliveries: webhooks.NewDeliveryCache()}
	secret, _ := crypto.Encrypt(e.s.encKey, "whsec")
	gs, err := st.CreateGitSource(store.GitSource{
		Provider: "github", RepoURL: "https://github.com/acme/repo.git", CloneMethod: "deploy_key",
		WebhookSecretEnc: secret, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		t.Fatal(err)
	}
	e.gs = gs

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		e.hits[r.URL.Path]++
		e.auth = r.Header.Get("Authorization")
		switch {
		case r.URL.Path == "/repos/acme/repo":
			fmt.Fprint(w, `{"full_name":"acme/repo","private":true}`)
		case r.URL.Path == "/repos/acme/repo/actions/workflows/deploymate.yml/runs":
			fmt.Fprintf(w, `{"workflow_runs":%s}`, e.runs)
		case r.URL.Path == "/user/repos":
			fmt.Fprintf(w, "[%s]", strings.Join(e.repo, ","))
		default:
			http.NotFound(w, r)
		}
	})
	gh := httptest.NewServer(mux)
	t.Cleanup(gh.Close)
	e.s.SetGitHubAPI(gh.URL)
	return e
}

func run(id int64, number int, event, headRepo string) string {
	return fmt.Sprintf(`{"id":%d,"run_number":%d,"event":%q,"conclusion":"success","head_branch":"main","head_sha":"sha%d","head_commit":{"message":"msg %d"},"head_repository":{"full_name":%q}}`,
		id, number, event, number, number, headRepo)
}

// post submits a form as the logged-in owner and returns status, Location.
func (e *prebuiltEnv) post(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	form.Set("csrf_token", "csrf")
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Header().Get("Location")
}

func flashOf(t *testing.T, loc string) string {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("flash")
}

func (e *prebuiltEnv) reload(t *testing.T) (store.App, store.GitSource) {
	t.Helper()
	app, err := e.st.GetAppBySlug("api")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := e.st.GetGitSource(e.gs.ID)
	if err != nil {
		t.Fatal(err)
	}
	return app, gs
}

func (e *prebuiltEnv) enablePrebuilt(t *testing.T) {
	t.Helper()
	enc, _ := crypto.Encrypt(e.s.encKey, testToken)
	if err := e.st.SetGitSourceAPIToken(e.gs.ID, enc); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateAppDeployMode(e.app.ID, store.DeployModeArtifact, "", ""); err != nil {
		t.Fatal(err)
	}
}

// TestDeployModeSave: prebuilt needs a token; the token is stored encrypted
// (never the plaintext), a blank field keeps it, "remove" clears it, and bad
// workflow / artifact names are refused without saving anything.
func TestDeployModeSave(t *testing.T) {
	e := newPrebuiltEnv(t)

	// No token yet -> refused, app stays in build mode.
	_, loc := e.post(t, "/apps/api/deploy-mode", url.Values{"mode": {"artifact"}})
	if !strings.Contains(flashOf(t, loc), "needs a GitHub token") {
		t.Errorf("flash = %q", flashOf(t, loc))
	}
	if app, _ := e.reload(t); app.DeployMode != store.DeployModeBuild {
		t.Fatalf("mode changed without a token: %q", app.DeployMode)
	}

	// With a token -> saved, defaults filled, token encrypted at rest.
	e.post(t, "/apps/api/deploy-mode", url.Values{"mode": {"artifact"}, "api_token": {testToken}})
	app, gs := e.reload(t)
	if app.DeployMode != store.DeployModeArtifact || app.WorkflowPath != store.DefaultWorkflowPath || app.ArtifactName != store.DefaultArtifactName {
		t.Errorf("app = %+v", app)
	}
	if gs.APITokenEnc == "" || strings.Contains(gs.APITokenEnc, testToken) {
		t.Errorf("token not stored encrypted: %q", gs.APITokenEnc)
	}
	if plain, err := crypto.Decrypt(e.s.encKey, gs.APITokenEnc); err != nil || plain != testToken {
		t.Errorf("token round trip: %q, %v", plain, err)
	}

	// Blank token keeps the stored one; custom names are saved.
	e.post(t, "/apps/api/deploy-mode", url.Values{"mode": {"artifact"}, "workflow_path": {".github/workflows/ci.yaml"}, "artifact_name": {"jar_1"}})
	app, gs2 := e.reload(t)
	if gs2.APITokenEnc != gs.APITokenEnc || app.WorkflowPath != ".github/workflows/ci.yaml" || app.ArtifactName != "jar_1" {
		t.Errorf("blank token / custom names: app=%+v tokenKept=%v", app, gs2.APITokenEnc == gs.APITokenEnc)
	}

	// Bad names are refused and change nothing.
	for _, bad := range []url.Values{
		{"mode": {"artifact"}, "workflow_path": {"../evil.yml"}},
		{"mode": {"artifact"}, "workflow_path": {".github/workflows/../../x.yml"}},
		{"mode": {"artifact"}, "workflow_path": {"deploy.yml"}},
		{"mode": {"artifact"}, "artifact_name": {"has space"}},
		{"mode": {"artifact"}, "artifact_name": {"a/b"}},
		{"mode": {"nonsense"}},
		{"mode": {"artifact"}, "api_token": {"two words"}},
	} {
		e.post(t, "/apps/api/deploy-mode", bad)
		if a, _ := e.reload(t); a.WorkflowPath != ".github/workflows/ci.yaml" || a.ArtifactName != "jar_1" {
			t.Errorf("bad form %v changed the app: %+v", bad, a)
		}
	}

	// Back to build mode with the token removed.
	e.post(t, "/apps/api/deploy-mode", url.Values{"mode": {"build"}, "clear_token": {"on"}})
	app, gs = e.reload(t)
	if app.DeployMode != store.DeployModeBuild || gs.APITokenEnc != "" {
		t.Errorf("after switch back: mode=%q token=%q", app.DeployMode, gs.APITokenEnc)
	}
}

func TestDeployModeNeedsGitHubRepo(t *testing.T) {
	e := newPrebuiltEnv(t)
	other, err := e.st.CreateGitSource(store.GitSource{Provider: "gitlab", RepoURL: "https://gitlab.com/acme/repo.git", CloneMethod: "deploy_key", DefaultBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateAppGitSource(e.app.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	_, loc := e.post(t, "/apps/api/deploy-mode", url.Values{"mode": {"artifact"}, "api_token": {testToken}})
	if !strings.Contains(flashOf(t, loc), "GitHub repository") {
		t.Errorf("flash = %q", flashOf(t, loc))
	}
	if app, _ := e.reload(t); app.DeployMode != store.DeployModeBuild {
		t.Error("a GitLab app switched to prebuilt mode")
	}
}

// TestGitTestConnection: reachable repo + workflow found, with the scope
// warning when the token can read other private repos, and clear messages
// for the failure modes.
func TestGitTestConnection(t *testing.T) {
	e := newPrebuiltEnv(t)
	e.enablePrebuilt(t)
	e.runs = "[" + run(11, 3, "push", "acme/repo") + "]"

	// Wide token: two OTHER private repos visible (a public one and this
	// repo itself do not count).
	e.repo = []string{
		`{"full_name":"acme/repo","private":true}`,
		`{"full_name":"acme/secret1","private":true}`,
		`{"full_name":"acme/secret2","private":true}`,
		`{"full_name":"acme/public","private":false}`,
	}
	_, loc := e.post(t, "/apps/api/git/test", url.Values{})
	f := flashOf(t, loc)
	for _, want := range []string{"Connected to acme/repo", "1 successful run", "2 other private repositories", "Only select repositories"} {
		if !strings.Contains(f, want) {
			t.Errorf("flash lacks %q: %s", want, f)
		}
	}
	if e.auth != "Bearer "+testToken {
		t.Errorf("fake GitHub saw Authorization %q", e.auth)
	}
	if strings.Contains(f, testToken) {
		t.Error("the token leaked into the flash message")
	}

	// Narrow token: no warning.
	e.repo = []string{`{"full_name":"acme/repo","private":true}`}
	_, loc = e.post(t, "/apps/api/git/test", url.Values{})
	if f := flashOf(t, loc); !strings.Contains(f, "scoped to this repository") || strings.Contains(f, "⚠") {
		t.Errorf("narrow token flash = %q", f)
	}

	// No token saved.
	if err := e.st.SetGitSourceAPIToken(e.gs.ID, ""); err != nil {
		t.Fatal(err)
	}
	_, loc = e.post(t, "/apps/api/git/test", url.Values{})
	if f := flashOf(t, loc); !strings.Contains(f, "Save a GitHub token first") {
		t.Errorf("no-token flash = %q", f)
	}
}

func TestGitTestFailureMessages(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "wrong or expired"},
		{403, "organization has not approved"},
		{404, "was not found"},
	} {
		e := newPrebuiltEnv(t)
		e.enablePrebuilt(t)
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			fmt.Fprint(w, `{"message":"secret-ish upstream text"}`)
		}))
		t.Cleanup(bad.Close)
		e.s.SetGitHubAPI(bad.URL)
		_, loc := e.post(t, "/apps/api/git/test", url.Values{})
		f := flashOf(t, loc)
		if !strings.Contains(f, tc.want) {
			t.Errorf("status %d: flash = %q, want %q", tc.status, f, tc.want)
		}
		if strings.Contains(f, "upstream text") {
			t.Errorf("status %d: GitHub's raw message was echoed: %q", tc.status, f)
		}
	}
}

// TestDeployLatestRun: the newest eligible successful run is queued as a
// dashboard-triggered deployment through the SAME gates a webhook uses.
func TestDeployLatestRun(t *testing.T) {
	e := newPrebuiltEnv(t)

	// Build-mode apps refuse.
	_, loc := e.post(t, "/apps/api/git/deploy-latest", url.Values{})
	if !strings.Contains(flashOf(t, loc), "switch it to prebuilt") {
		t.Errorf("build-mode flash = %q", flashOf(t, loc))
	}

	e.enablePrebuilt(t)

	// No runs yet.
	e.runs = "[]"
	_, loc = e.post(t, "/apps/api/git/deploy-latest", url.Values{})
	if !strings.Contains(flashOf(t, loc), "No successful run") {
		t.Errorf("no-run flash = %q", flashOf(t, loc))
	}

	// A fork's run (newest) is skipped; the repo's own run underneath deploys.
	e.runs = "[" + run(30, 9, "push", "evil/repo") + "," + run(20, 8, "push", "acme/repo") + "," + run(10, 7, "push", "acme/repo") + "]"
	code, loc := e.post(t, "/apps/api/git/deploy-latest", url.Values{})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/deployments/") {
		t.Fatalf("deploy-latest: %d %q", code, loc)
	}
	ds, _ := e.st.ListDeployments(e.app.ID, 5)
	if len(ds) != 1 {
		t.Fatalf("deployments = %d, want 1", len(ds))
	}
	d := ds[0]
	if d.CIRun != 20 || d.CIRunNumber != 8 || d.Trigger != "dashboard" || d.Status != "queued" || d.CommitSHA != "sha8" || d.CommitMessage != "msg 8" {
		t.Errorf("deployment = %+v", d)
	}

	// Pressing it again: run 20 is already handled, and the older run 10 must
	// not be deployed instead.
	_, loc = e.post(t, "/apps/api/git/deploy-latest", url.Values{})
	if f := flashOf(t, loc); !strings.Contains(f, "already handled") {
		t.Errorf("repeat flash = %q", f)
	}
	if ds, _ := e.st.ListDeployments(e.app.ID, 5); len(ds) != 1 {
		t.Errorf("repeat queued another deployment (%d)", len(ds))
	}
}

// The review page's deploy button posts to /git/deploy; for a prebuilt app
// that must deploy the latest CI run, not queue a clone-and-build.
func TestGitDeployDelegatesForPrebuiltApps(t *testing.T) {
	e := newPrebuiltEnv(t)
	e.enablePrebuilt(t)
	e.runs = "[" + run(5, 2, "workflow_dispatch", "acme/repo") + "]"
	_, loc := e.post(t, "/apps/api/git/deploy", url.Values{"sha": {"deadbeef"}})
	if !strings.HasPrefix(loc, "/deployments/") {
		t.Fatalf("location = %q", loc)
	}
	ds, _ := e.st.ListDeployments(e.app.ID, 5)
	if len(ds) != 1 || ds[0].CIRun != 5 || ds[0].CommitSHA == "deadbeef" {
		t.Errorf("deployments = %+v", ds)
	}
}

// TestAppPageShowsPrebuiltPanel: the page offers the mode form for GitHub
// sources, shows the generated workflow once prebuilt, and never renders the
// token.
func TestAppPageShowsPrebuiltPanel(t *testing.T) {
	e := newPrebuiltEnv(t)
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/apps/api", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /apps/api: %d", rec.Code)
		}
		return rec.Body.String()
	}
	// Needs a real key for the page's deploy-key readout.
	key, _ := crypto.Encrypt(e.s.encKey, testPEM(t))
	if _, err := e.st.DB().Exec(`UPDATE git_sources SET private_key_enc = ? WHERE id = ?`, key, e.gs.ID); err != nil {
		t.Fatal(err)
	}

	body := get()
	if !strings.Contains(body, `name="mode"`) || !strings.Contains(body, "Build on this server") || !strings.Contains(body, "Review &amp; deploy") {
		t.Error("build-mode page lacks the mode form / Review & deploy")
	}
	if strings.Contains(body, "retention-days") {
		t.Error("workflow file shown before prebuilt mode is on")
	}

	e.enablePrebuilt(t)
	body = get()
	for _, want := range []string{"Deploy latest successful run", "Test connection", "retention-days: 1", "./gradlew bootJar", "./mvnw", "saved — leave blank to keep", "Workflow runs"} {
		if !strings.Contains(body, want) {
			t.Errorf("prebuilt page lacks %q", want)
		}
	}
	if strings.Contains(body, testToken) {
		t.Error("the stored token is rendered on the page")
	}
}

func testPEM(t *testing.T) string {
	t.Helper()
	k, err := gitpkg.GenerateDeployKey()
	if err != nil {
		t.Skipf("cannot generate a deploy key here: %v", err)
	}
	return k.PrivateKeyPEM
}
