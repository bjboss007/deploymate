package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// buildTestRemote makes a work repo + bare remote with three commits and
// returns the remote path + the shas (mirror_test.go's twin in this
// package). Deploying the first commit ships two more.
func buildTestRemote(t *testing.T) (remote, first, second, third string) {
	t.Helper()
	work := t.TempDir()
	gt := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gt("init", "-b", "main")
	gt("config", "user.email", "t@test.dev")
	gt("config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt("add", ".")
	gt("commit", "-m", "first commit")
	first = gt("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("zero\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt("add", ".")
	gt("commit", "-m", "second commit")
	second = gt("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(work, "c.txt"), []byte("c\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gt("add", ".")
	gt("commit", "-m", "third commit")
	third = gt("rev-parse", "HEAD")
	remote = filepath.Join(t.TempDir(), "remote.git")
	gt("clone", "--bare", work, remote)
	return remote, first, second, third
}

// seedGitApp returns a logged-in server (with dataDir + encKey set) whose
// app is connected to the test remote, with the FIRST commit as the current
// deployment.
func seedGitApp(t *testing.T) (*Server, store.App, string, string) {
	t.Helper()
	remote, first, _, third := buildTestRemote(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	encKey := [32]byte{7}
	keyEnc, err := crypto.Encrypt(encKey, "test-key")
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	gs, err := st.CreateGitSource(store.GitSource{
		Provider: "github", RepoURL: remote, CloneMethod: "deploy_key",
		PrivateKeyEnc: keyEnc, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("create git source: %v", err)
	}
	app, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Web", Slug: "web", GitSourceID: gs.ID})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	cur, err := st.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "running", Trigger: "webhook", CommitSHA: first,
	})
	if err != nil {
		t.Fatalf("create current deployment: %v", err)
	}
	if err := st.SetAppCurrentDeployment(app.ID, cur.ID); err != nil {
		t.Fatalf("set current: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return &Server{store: st, encKey: encKey, dataDir: t.TempDir()}, app, first, third
}

// TestDeployPreviewShowsRange drives the review page: it must show the
// commit stats vs the deployed commit and pin that SHA in the confirm form.
func TestDeployPreviewShowsRange(t *testing.T) {
	s, _, _, head := seedGitApp(t)
	req := httptest.NewRequest(http.MethodGet, "/apps/web/deploy-preview", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Review before deploying", ">2<", ">3<", "Commits", "Files changed", "third commit", "second commit", "T", head[:8]} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
	// The confirm form must pin exactly the reviewed SHA.
	if !strings.Contains(body, `name="sha" value="`+head+`"`) {
		t.Fatalf("confirm form missing pinned sha %s", head)
	}
}

// TestDeployPreviewFirstDeploy shows the first-deploy branch when nothing
// has shipped yet.
func TestDeployPreviewFirstDeploy(t *testing.T) {
	s, app, _, _ := seedGitApp(t)
	if err := s.store.UpdateAppStatus(app.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	// Detach the current deployment: an app that never deployed has none.
	if err := s.store.SetAppCurrentDeployment(app.ID, ""); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/apps/web/deploy-preview", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "first deploy") {
		t.Fatalf("first-deploy page missing banner: %s", body)
	}
}

// TestGitDeployPinsReviewedSHA proves the sha form field pins the queued
// deployment to exactly what was reviewed.
func TestGitDeployPinsReviewedSHA(t *testing.T) {
	s, _, _, head := seedGitApp(t)
	form := url.Values{"sha": {head}, "csrf_token": {"csrf"}}
	req := httptest.NewRequest(http.MethodPost, "/apps/web/git/deploy", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	id := strings.TrimPrefix(loc, "/deployments/")
	d, err := s.store.GetDeployment(id)
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if d.CommitSHA != head {
		t.Fatalf("pinned sha = %s, want %s", d.CommitSHA, head)
	}
	if d.Trigger != "dashboard" {
		t.Fatalf("trigger = %q, want dashboard", d.Trigger)
	}
}
