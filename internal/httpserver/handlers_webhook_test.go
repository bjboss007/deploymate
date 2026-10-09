package httpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
)

// webhookEnv is a Server plus apps, each linked to its OWN github git
// source (the dev/stage/prod-on-one-repo shape: one webhook per app).
type webhookEnv struct {
	s       *Server
	st      *store.Store
	secrets map[string]string // source id -> webhook secret
}

func newWebhookEnv(t *testing.T) *webhookEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &webhookEnv{
		s:       &Server{store: st, encKey: [32]byte{7}, deliveries: webhooks.NewDeliveryCache()},
		st:      st,
		secrets: map[string]string{},
	}
}

// addApp creates an app with its own source tracking branch; returns the
// source id (the /hooks/{id} path segment) and the app.
func (e *webhookEnv) addApp(t *testing.T, slug, branch string) (string, store.App) {
	t.Helper()
	owner, err := e.st.GetUserByEmail("owner@test.dev")
	if err != nil {
		owner, err = e.st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
		if err != nil {
			t.Fatal(err)
		}
	}
	proj, err := e.st.GetProjectBySlug(owner.ID, "test")
	if err != nil {
		proj, err = e.st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
		if err != nil {
			t.Fatal(err)
		}
	}
	app, err := e.st.CreateApp(store.App{ProjectID: proj.ID, Name: slug, Slug: slug, Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	secret := "secret-" + slug
	enc, err := crypto.Encrypt(e.s.encKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := e.st.CreateGitSource(store.GitSource{
		Provider: "github", RepoURL: "https://github.com/acme/repo", CloneMethod: "deploy_key",
		WebhookSecretEnc: enc, DefaultBranch: branch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		t.Fatal(err)
	}
	e.secrets[gs.ID] = secret
	return gs.ID, app
}

// deliver posts a signed GitHub webhook; event "" omits the header (an old
// manual replay). Returns the status code and the response body.
func (e *webhookEnv) deliver(t *testing.T, sourceID, event, guid, body string) (int, string) {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(e.secrets[sourceID]))
	mac.Write([]byte(body))
	req := httptest.NewRequest(http.MethodPost, "/hooks/"+sourceID, strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if guid != "" {
		req.Header.Set("X-GitHub-Delivery", guid)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func (e *webhookEnv) deployments(t *testing.T, app store.App) int {
	t.Helper()
	ds, err := e.st.ListDeployments(app.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return len(ds)
}

const pushMain = `{"ref":"refs/heads/main","after":"abc123","head_commit":{"message":"m"}}`

// TestWebhookFanOutSameDeliveryGUID is the live bug (2026-09-04): GitHub
// sends every webhook on a repo the SAME X-GitHub-Delivery GUID for one
// push. Each env's app has its own source+hook, so each must deploy — the
// de-dupe is per source, not global.
func TestWebhookFanOutSameDeliveryGUID(t *testing.T) {
	e := newWebhookEnv(t)
	devSrc, dev := e.addApp(t, "app-dev", "main")
	stageSrc, stage := e.addApp(t, "app-stage", "main")
	prodSrc, prod := e.addApp(t, "app-prod", "main")

	for _, c := range []struct {
		src string
		app store.App
	}{{devSrc, dev}, {stageSrc, stage}, {prodSrc, prod}} {
		code, body := e.deliver(t, c.src, "push", "same-guid-for-all", pushMain)
		if code != 200 || body != "queued" {
			t.Fatalf("%s: %d %q, want 200 queued", c.app.Slug, code, body)
		}
		if n := e.deployments(t, c.app); n != 1 {
			t.Errorf("%s has %d deployments, want 1", c.app.Slug, n)
		}
	}
}

// TestWebhookRetryOnSameSourceIsDeduped: a provider retry (same source, same
// GUID) must still deploy only once.
func TestWebhookRetryOnSameSourceIsDeduped(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.addApp(t, "app-dev", "main")
	if code, body := e.deliver(t, src, "push", "g1", pushMain); code != 200 || body != "queued" {
		t.Fatalf("first: %d %q", code, body)
	}
	if code, body := e.deliver(t, src, "push", "g1", pushMain); code != 200 || body != "duplicate delivery ignored" {
		t.Errorf("retry: %d %q, want duplicate ignored", code, body)
	}
	if n := e.deployments(t, app); n != 1 {
		t.Errorf("deployments = %d, want 1", n)
	}
	// A different GUID is a new push and deploys again.
	if code, body := e.deliver(t, src, "push", "g2", pushMain); code != 200 || body != "queued" {
		t.Errorf("new guid: %d %q", code, body)
	}
}

// TestWebhookEventDispatch: ping answers pong (still authenticated), event
// types that are not pushes never deploy, and a delivery with no event
// header (an old manual replay of a signed push) keeps working.
func TestWebhookEventDispatch(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.addApp(t, "app-dev", "main")

	if code, body := e.deliver(t, src, "ping", "p1", `{"zen":"Keep it logically awesome."}`); code != 200 || body != "pong" {
		t.Errorf("ping: %d %q, want 200 pong", code, body)
	}
	// workflow_run is routed in a later phase (prebuilt deploys); until then
	// it must be a clean no-op, not parsed as a push.
	wf := `{"action":"completed","workflow_run":{"id":1,"head_branch":"main","conclusion":"success"}}`
	if code, body := e.deliver(t, src, "workflow_run", "w1", wf); code != 200 || !strings.HasPrefix(body, "ignored:") {
		t.Errorf("workflow_run: %d %q, want 200 ignored", code, body)
	}
	if code, body := e.deliver(t, src, "issues", "i1", `{"action":"opened"}`); code != 200 || !strings.HasPrefix(body, "ignored:") {
		t.Errorf("issues: %d %q, want 200 ignored", code, body)
	}
	if n := e.deployments(t, app); n != 0 {
		t.Fatalf("non-push events queued %d deployments, want 0", n)
	}

	// Legacy replay: no X-GitHub-Event header → treated as a push.
	if code, body := e.deliver(t, src, "", "r1", pushMain); code != 200 || body != "queued" {
		t.Errorf("headerless push: %d %q, want queued", code, body)
	}
	// A wrong-branch push is still filtered.
	if code, body := e.deliver(t, src, "push", "b1", `{"ref":"refs/heads/other","after":"x"}`); body != "ignored: not the deploy branch" {
		t.Errorf("wrong branch: %d %q", code, body)
	}
}

// TestWebhookPingStillAuthenticated: a ping with a bad signature is
// rejected like any other delivery (ping must not become an oracle).
func TestWebhookPingStillAuthenticated(t *testing.T) {
	e := newWebhookEnv(t)
	src, _ := e.addApp(t, "app-dev", "main")
	req := httptest.NewRequest(http.MethodPost, "/hooks/"+src, strings.NewReader(`{"zen":"x"}`))
	req.Header.Set("X-GitHub-Event", "ping")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("bad-signature ping = %d, want 401", rec.Code)
	}
}

// A push that changed nothing under the app's build folder must not deploy it;
// one that did, or one the payload can't describe, must.
func TestWebhookSkipsPushesOutsideTheBuildFolder(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.addApp(t, "site", "main")
	if err := e.st.UpdateAppRootDirectory(app.ID, "site"); err != nil {
		t.Fatal(err)
	}
	push := func(commits string) string {
		return `{"ref":"refs/heads/main","after":"abc12345","head_commit":{"message":"m"},"commits":` + commits + `}`
	}
	goOnly := push(`[{"modified":["internal/x.go"],"added":["README.md"]}]`)
	if code, body := e.deliver(t, src, "push", "s1", goOnly); code != 200 || body != "ignored: no changes in the build folder" {
		t.Errorf("unrelated push: %d %q", code, body)
	}
	if n := e.deployments(t, app); n != 0 {
		t.Fatalf("unrelated push queued %d deployments", n)
	}
	evs, _ := e.st.ListEvents(app.ID, 10)
	found := false
	for _, ev := range evs {
		found = found || ev.Kind == "deploy_skipped"
	}
	if !found {
		t.Error("no deploy_skipped event recorded")
	}
	// "site-old/x" is a different folder, not a child of "site".
	if _, body := e.deliver(t, src, "push", "s2", push(`[{"modified":["site-old/a.html"]}]`)); body != "ignored: no changes in the build folder" {
		t.Errorf("sibling-prefix push: %q", body)
	}
	if _, body := e.deliver(t, src, "push", "s3", push(`[{"modified":["internal/x.go"]},{"removed":["site/old.html"]}]`)); body != "queued" {
		t.Errorf("push touching the folder: %q", body)
	}
	// No commit list → can't tell → deploy.
	if _, body := e.deliver(t, src, "push", "s4", pushMain); body != "queued" {
		t.Errorf("push without file info: %q", body)
	}
}

// A push that deletes the branch has nothing to deploy; it used to queue a deploy
// of the all-zero commit, which could only fail.
func TestWebhookIgnoresADeletedBranch(t *testing.T) {
	e := newWebhookEnv(t)
	src, app := e.addApp(t, "app-dev", "main")
	body := `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000","deleted":true}`
	if code, got := e.deliver(t, src, "push", "del1", body); code != 200 || got != "ignored: branch deleted" {
		t.Errorf("deleted branch: %d %q", code, got)
	}
	if n := e.deployments(t, app); n != 0 {
		t.Errorf("%d deployments queued for a deleted branch", n)
	}
}
