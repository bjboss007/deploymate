package httpserver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/githubapp"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeGitHub answers the two calls "Connect GitHub" makes.
type fakeGitHub struct {
	*httptest.Server
	key        *rsa.PrivateKey
	pemKey     string
	tokenCalls int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{key: k, pemKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))}
	mux := http.NewServeMux()
	mux.HandleFunc("/app-manifests/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/app-manifests/good-code/") {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": 4242, "slug": "deploymate-test", "name": "DeployMate test", "html_url": "https://github.com/apps/deploymate-test",
			"pem": f.pemKey, "webhook_secret": "the-webhook-secret", "client_id": "Iv1.abc", "client_secret": "the-client-secret",
			"owner": map[string]string{"login": "habib", "type": "User"},
		})
	})
	mux.HandleFunc("/app/installations/", func(w http.ResponseWriter, r *http.Request) { // POST .../{id}/access_tokens
		iss, err := githubapp.VerifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &k.PublicKey, time.Now())
		if err != nil || iss != 4242 || r.Method != http.MethodPost {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"bad jwt"}`))
			return
		}
		f.tokenCalls++
		switch {
		case strings.Contains(r.URL.Path, "/9/"):
			w.Write([]byte(`{"token":"ghs_nine","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		case strings.Contains(r.URL.Path, "/10/"):
			w.Write([]byte(`{"token":"ghs_ten","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer ghs_nine":
			w.Write([]byte(`{"total_count":2,"repositories":[
				{"id":1,"full_name":"habib/site","private":true,"default_branch":"main","clone_url":"https://github.com/habib/site.git"},
				{"id":2,"full_name":"habib/old","private":false,"default_branch":"main","clone_url":"https://github.com/habib/old.git","archived":true}]}`))
		case "Bearer ghs_ten":
			w.Write([]byte(`{"total_count":1,"repositories":[{"id":3,"full_name":"acme/web","private":false,"default_branch":"develop","clone_url":"https://github.com/acme/web.git"}]}`))
		default:
			w.WriteHeader(401)
		}
	})
	mux.HandleFunc("/repos/", func(w http.ResponseWriter, r *http.Request) { // branches
		ok := map[string]bool{"/repos/habib/site/branches/main": true, "/repos/habib/site/branches/feature/x": true, "/repos/acme/web/branches/develop": true}
		if ok[r.URL.Path] && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_") {
			w.Write([]byte(`{"name":"x"}`))
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Branch not found"}`))
	})
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		iss, err := githubapp.VerifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &k.PublicKey, time.Now())
		if err != nil || iss != 4242 {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"bad jwt"}`))
			return
		}
		w.Write([]byte(`[{"id":9,"repository_selection":"selected","account":{"login":"habib","type":"User"}},
			{"id":10,"repository_selection":"all","account":{"login":"acme","type":"Organization"}}]`))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

type ghEnv struct {
	*webhookEnv
	gh *fakeGitHub
}

func newGHEnv(t *testing.T) *ghEnv {
	t.Helper()
	e := newWebhookEnv(t)
	gh := newFakeGitHub(t)
	e.s.SetGitHubAPI(gh.URL)
	e.s.SetGitHubWeb("https://github.example")
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if _, err := e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	return &ghEnv{e, gh}
}

func (e *ghEnv) req(method, path, host string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body *strings.Reader
	if form != nil {
		form.Set("csrf_token", "csrf")
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if host != "" {
		r.Host = host
	}
	r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, r)
	return rec
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// connect runs the whole flow and leaves the app connected.
func (e *ghEnv) connect(t *testing.T, host string) {
	t.Helper()
	rec := e.req(http.MethodPost, "/settings/github/connect", host, url.Values{"kind": {"user"}})
	st := cookieNamed(rec, ghStateCookie)
	if st == nil {
		t.Fatalf("no state cookie: %d %s", rec.Code, rec.Body.String())
	}
	cb := e.req(http.MethodGet, "/settings/github/callback?code=good-code&state="+url.QueryEscape(st.Value), host, nil, &http.Cookie{Name: ghStateCookie, Value: st.Value})
	if cb.Code != http.StatusSeeOther || !strings.Contains(cb.Header().Get("Location"), "Connected") {
		t.Fatalf("callback: %d %s", cb.Code, cb.Header().Get("Location"))
	}
}

func TestGitHubConnectStartsTheManifestFlow(t *testing.T) {
	e := newGHEnv(t)
	if body := e.req(http.MethodGet, "/settings/github", "dm.example.com", nil).Body.String(); !strings.Contains(body, "Connect GitHub") || strings.Contains(body, "isn't reachable") {
		t.Errorf("a public address shows the button without a warning")
	}
	if body := e.req(http.MethodGet, "/settings/github", "127.0.0.1:8080", nil).Body.String(); !strings.Contains(body, "isn&#39;t reachable from the internet") && !strings.Contains(body, "isn't reachable from the internet") {
		t.Errorf("a loopback address must warn that the webhook can't work")
	}

	rec := e.req(http.MethodPost, "/settings/github/connect", "dm.example.com", url.Values{"kind": {"user"}})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `action="https://github.example/settings/apps/new?state=`) {
		t.Fatalf("redirect page: %d %.300s", rec.Code, body)
	}
	for _, want := range []string{"redirect_url", "http://dm.example.com/settings/github/callback", "http://dm.example.com/hooks/github-app", "&#34;active&#34;:true"} {
		if !strings.Contains(body, want) {
			t.Errorf("manifest missing %q", want)
		}
	}
	c := cookieNamed(rec, ghStateCookie)
	if c == nil || !c.HttpOnly || c.Value == "" || !strings.Contains(body, url.QueryEscape(c.Value)) {
		t.Errorf("state cookie missing or not bound to the form: %+v", c)
	}

	// An organisation goes to the organisation's page; a bad name is refused.
	org := e.req(http.MethodPost, "/settings/github/connect", "dm.example.com", url.Values{"kind": {"org"}, "org": {"acme-co"}}).Body.String()
	if !strings.Contains(org, "https://github.example/organizations/acme-co/settings/apps/new") {
		t.Error("organisation URL wrong")
	}
	bad := e.req(http.MethodPost, "/settings/github/connect", "dm.example.com", url.Values{"kind": {"org"}, "org": {"a/../b"}})
	if bad.Code != http.StatusSeeOther || cookieNamed(bad, ghStateCookie) != nil {
		t.Errorf("a bad organisation name must be refused (%d)", bad.Code)
	}

	// Without a public address the webhook is created switched off.
	loc := e.req(http.MethodPost, "/settings/github/connect", "127.0.0.1:8080", url.Values{"kind": {"user"}}).Body.String()
	if !strings.Contains(loc, "&#34;active&#34;:false") {
		t.Error("the webhook must be inactive on a loopback address")
	}
}

func TestGitHubCallbackRejectsWrongStateAndStoresTheApp(t *testing.T) {
	e := newGHEnv(t)

	// No cookie / wrong state: nothing is stored, GitHub is never asked.
	for name, c := range map[string]*http.Cookie{"no cookie": nil, "wrong state": {Name: ghStateCookie, Value: "other"}} {
		var cookies []*http.Cookie
		if c != nil {
			cookies = append(cookies, c)
		}
		rec := e.req(http.MethodGet, "/settings/github/callback?code=good-code&state=abc", "dm.example.com", nil, cookies...)
		if _, err := e.st.GetGitHubApp(); err == nil {
			t.Fatalf("%s: an app was stored", name)
		}
		if !strings.Contains(rec.Header().Get("Location"), "did+not+start+here") {
			t.Errorf("%s: %s", name, rec.Header().Get("Location"))
		}
	}

	e.connect(t, "dm.example.com")
	app, err := e.st.GetGitHubApp()
	if err != nil || app.AppID != 4242 || app.Slug != "deploymate-test" || app.OwnerLogin != "habib" {
		t.Fatalf("stored: %+v %v", app, err)
	}
	for _, secret := range []string{"the-webhook-secret", "the-client-secret", "BEGIN RSA PRIVATE KEY"} {
		for _, stored := range []string{app.WebhookSecretEnc, app.ClientSecretEnc, app.PEMEnc} {
			if strings.Contains(stored, secret) {
				t.Errorf("%q is stored in plaintext", secret)
			}
		}
	}
	if app.WebhookURL != "http://dm.example.com/hooks/github-app" {
		t.Errorf("webhook url = %q", app.WebhookURL)
	}

	page := e.req(http.MethodGet, "/settings/github", "dm.example.com", nil).Body.String()
	for _, want := range []string{"DeployMate test", "habib", "all repositories", "selected repositories", "acme", "Install on GitHub", "https://github.example/apps/deploymate-test/installations/new"} {
		if !strings.Contains(page, want) {
			t.Errorf("connected page missing %q", want)
		}
	}
	for _, leak := range []string{"the-webhook-secret", "the-client-secret", "PRIVATE KEY"} {
		if strings.Contains(page, leak) {
			t.Errorf("the page shows %q", leak)
		}
	}

	// A second connect is refused; the old state cookie can't be replayed.
	again := e.req(http.MethodPost, "/settings/github/connect", "dm.example.com", url.Values{"kind": {"user"}})
	if cookieNamed(again, ghStateCookie) != nil {
		t.Error("connecting twice must not start another flow")
	}

	// Disconnect forgets it.
	if rec := e.req(http.MethodPost, "/settings/github/disconnect", "dm.example.com", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("disconnect: %d", rec.Code)
	}
	if _, err := e.st.GetGitHubApp(); err == nil {
		t.Error("the app is still stored after disconnecting")
	}
}

func TestGitHubCallbackWithAStaleCodeStoresNothing(t *testing.T) {
	e := newGHEnv(t)
	st := cookieNamed(e.req(http.MethodPost, "/settings/github/connect", "dm.example.com", url.Values{"kind": {"user"}}), ghStateCookie)
	rec := e.req(http.MethodGet, "/settings/github/callback?code=expired&state="+url.QueryEscape(st.Value), "dm.example.com", nil, &http.Cookie{Name: ghStateCookie, Value: st.Value})
	if !strings.Contains(rec.Header().Get("Location"), "did+not+accept") {
		t.Errorf("location %q", rec.Header().Get("Location"))
	}
	if _, err := e.st.GetGitHubApp(); err == nil {
		t.Error("an app was stored from a rejected code")
	}
}

func TestGitHubPagesNeedASession(t *testing.T) {
	e := newGHEnv(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/settings/github"}, {http.MethodGet, "/settings/github/callback?code=x&state=y"},
		{http.MethodPost, "/settings/github/connect"}, {http.MethodPost, "/settings/github/disconnect"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, r)
		if rec.Code == http.StatusOK {
			t.Errorf("%s %s answered 200 without a session", c.method, c.path)
		}
	}
}

func TestGitHubAppWebhookVerifiesItsSignature(t *testing.T) {
	e := newGHEnv(t)
	post := func(event, body, sig string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/hooks/github-app", strings.NewReader(body))
		r.Header.Set("X-GitHub-Event", event)
		if sig != "" {
			r.Header.Set("X-Hub-Signature-256", sig)
		}
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, r)
		return rec
	}
	sign := func(body string) string {
		m := hmac.New(sha256.New, []byte("the-webhook-secret"))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	if rec := post("ping", "{}", sign("{}")); rec.Code != http.StatusNotFound {
		t.Errorf("before connecting: %d, want 404", rec.Code)
	}
	e.connect(t, "dm.example.com")
	if rec := post("ping", "{}", sign("{}")); rec.Code != 200 || rec.Body.String() != "pong" {
		t.Errorf("signed ping: %d %q", rec.Code, rec.Body.String())
	}
	if rec := post("ping", "{}", "sha256=deadbeef"); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: %d, want 401", rec.Code)
	}
	if rec := post("ping", "{}", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no signature: %d, want 401", rec.Code)
	}
	if rec := post("push", `{"ref":"refs/heads/main"}`, sign(`{"ref":"refs/heads/main"}`)); rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "ignored") {
		t.Errorf("signed push (not handled yet): %d %q", rec.Code, rec.Body.String())
	}
	// The per-repository webhook route still works beside it.
	if rec := post("push", "{}", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unexpected: %d", rec.Code)
	}
}

// ---- repositories (phase 2) -------------------------------------------------

func (e *ghEnv) newApp(t *testing.T, slug string) store.App {
	t.Helper()
	owner, _ := e.st.GetUserByEmail("o@test.dev")
	proj, err := e.st.GetProjectBySlug(owner.ID, "gh")
	if err != nil {
		proj, err = e.st.CreateProject(store.Project{UserID: owner.ID, Name: "GH", Slug: "gh"})
		if err != nil {
			t.Fatal(err)
		}
	}
	app, err := e.st.CreateApp(store.App{ProjectID: proj.ID, Name: slug, Slug: slug, Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func (e *ghEnv) connectRepo(slug, repo, branch string) *httptest.ResponseRecorder {
	return e.req(http.MethodPost, "/apps/"+slug+"/git/github", "dm.example.com", url.Values{"repo": {repo}, "branch": {branch}})
}

func TestAppPageOffersTheRepositoriesTheAppCanSee(t *testing.T) {
	e := newGHEnv(t)
	e.newApp(t, "web")

	before := e.req(http.MethodGet, "/apps/web", "dm.example.com", nil).Body.String()
	if strings.Contains(before, "From GitHub") || !strings.Contains(before, "connect GitHub") {
		t.Error("without a connection the page should only hint at Connect GitHub")
	}

	e.connect(t, "dm.example.com")
	body := e.req(http.MethodGet, "/apps/web", "dm.example.com", nil).Body.String()
	for _, want := range []string{"From GitHub", `value="9:habib/site"`, "habib/site (private)", `value="10:acme/web"`, "No deploy key or webhook"} {
		if !strings.Contains(body, want) {
			t.Errorf("picker missing %q", want)
		}
	}
	if strings.Contains(body, "habib/old") {
		t.Error("an archived repository must not be offered")
	}
	if strings.Contains(body, "ghs_") {
		t.Error("an installation token reached the page")
	}
}

func TestConnectingAPickedRepositoryCreatesAnAppSourceWithoutKeys(t *testing.T) {
	e := newGHEnv(t)
	app := e.newApp(t, "web")
	e.connect(t, "dm.example.com")

	rec := e.connectRepo("web", "9:habib/site", "")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "Repository+connected") {
		t.Fatalf("connect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	got, _ := e.st.GetAppByID(app.ID)
	gs, err := e.st.GetGitSource(got.GitSourceID)
	if err != nil {
		t.Fatal(err)
	}
	if gs.CloneMethod != store.CloneGitHubApp || gs.InstallationID != 9 || gs.RepoFullName != "habib/site" ||
		gs.DefaultBranch != "main" || gs.RepoURL != "https://github.com/habib/site.git" || gs.PrivateKeyEnc != "" {
		t.Errorf("source: %+v", gs)
	}
	page := e.req(http.MethodGet, "/apps/web", "dm.example.com", nil).Body.String()
	for _, bad := range []string{"Deploy key", "Webhook secret", "Webhook URL"} {
		if i := strings.Index(page, bad); i >= 0 {
			t.Errorf("an app source must show no %q, found at: ...%s...", bad, page[max(0, i-120):min(len(page), i+80)])
		}
	}
	if !strings.Contains(page, "Through your GitHub app") {
		t.Error("the page should say the access is through the GitHub app")
	}
	if !strings.Contains(page, "habib/site") {
		t.Error("the repository should be shown")
	}
	// A branch with a slash and a non-default branch of another installation.
	app2 := e.newApp(t, "web2")
	if rec := e.connectRepo("web2", "9:habib/site", "feature/x"); !strings.Contains(rec.Header().Get("Location"), "Repository+connected") {
		t.Errorf("branch with a slash: %s", rec.Header().Get("Location"))
	}
	got2, _ := e.st.GetAppByID(app2.ID)
	if gs2, _ := e.st.GetGitSource(got2.GitSourceID); gs2.DefaultBranch != "feature/x" {
		t.Errorf("branch = %q", gs2.DefaultBranch)
	}
	app3 := e.newApp(t, "web3")
	e.connectRepo("web3", "10:acme/web", "")
	got3, _ := e.st.GetAppByID(app3.ID)
	if gs3, _ := e.st.GetGitSource(got3.GitSourceID); gs3.DefaultBranch != "develop" || gs3.InstallationID != 10 {
		t.Errorf("default branch of a repository should come from GitHub: %+v", gs3)
	}
}

func TestConnectingARepositoryRefusesWhatGitHubDoesNotShow(t *testing.T) {
	e := newGHEnv(t)
	e.newApp(t, "web")
	// Not connected to GitHub yet.
	if loc := e.connectRepo("web", "9:habib/site", "").Header().Get("Location"); !strings.Contains(loc, "not+connected") {
		t.Errorf("not connected: %s", loc)
	}
	e.connect(t, "dm.example.com")
	for name, c := range map[string]struct{ repo, branch, want string }{
		"a repository the installation can't see":                        {"9:evil/other", "", "can%27t+see"},
		"the right repository, the wrong installation":                   {"10:habib/site", "", "can%27t+see"},
		"an archived repository still visible to the API is not special": {"9:habib/old", "", ""}, // visible to the API, so allowed
		"a branch that does not exist":                                   {"9:habib/site", "nope", "does+not+exist"},
		"a malformed choice":                                             {"habib/site", "", "Pick+a+repository"},
		"an option injection in the branch":                              {"9:habib/site", "--upload-pack=x", "not+a+valid+branch"},
	} {
		app := e.newApp(t, strings.ReplaceAll(strings.ToLower(name[:8]), " ", "")+"x")
		rec := e.connectRepo(app.Slug, c.repo, c.branch)
		loc := rec.Header().Get("Location")
		if c.want == "" {
			continue
		}
		if !strings.Contains(loc, c.want) {
			t.Errorf("%s: %s, want it to contain %q", name, loc, c.want)
		}
		got, _ := e.st.GetAppByID(app.ID)
		if got.GitSourceID != "" {
			t.Errorf("%s: a git source was created", name)
		}
	}
	// Already connected.
	app := e.newApp(t, "twice")
	e.connectRepo("twice", "9:habib/site", "")
	if loc := e.connectRepo("twice", "9:habib/site", "").Header().Get("Location"); !strings.Contains(loc, "already") {
		t.Errorf("second connect: %s", loc)
	}
	_ = app
}

func TestInstallationTokensAreReusedAcrossRequests(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	e.newApp(t, "a1")
	e.newApp(t, "a2")
	e.connectRepo("a1", "9:habib/site", "")
	before := e.gh.tokenCalls
	e.connectRepo("a2", "9:habib/site", "main")
	if e.gh.tokenCalls != before {
		t.Errorf("a second connect minted %d more tokens; the cached one should be reused", e.gh.tokenCalls-before)
	}
}

// ---- push handling (phase 3) ------------------------------------------------

func (e *ghEnv) deliver(event, delivery, body string) string {
	r := httptest.NewRequest(http.MethodPost, "/hooks/github-app", strings.NewReader(body))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-GitHub-Delivery", delivery)
	m := hmac.New(sha256.New, []byte("the-webhook-secret"))
	m.Write([]byte(body))
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m.Sum(nil)))
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, r)
	return strings.TrimSpace(rec.Body.String())
}

func push(repo, ref, after string, files ...string) string {
	b, _ := json.Marshal(map[string]any{
		"ref": ref, "after": after, "head_commit": map[string]string{"message": "ship it"},
		"repository": map[string]string{"full_name": repo},
		"commits":    []map[string]any{{"modified": files}},
	})
	return string(b)
}

func (e *ghEnv) deployments(t *testing.T, appID string) []store.Deployment {
	t.Helper()
	ds, err := e.st.ListDeployments(appID, 20)
	if err != nil {
		t.Fatal(err)
	}
	return ds
}

func TestAPushToAConnectedRepositoryDeploysItsApps(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	web := e.newApp(t, "web")
	e.connectRepo("web", "9:habib/site", "")

	if got := e.deliver("push", "d1", push("habib/site", "refs/heads/main", "abc123", "x.go")); got != "queued" {
		t.Fatalf("push: %q", got)
	}
	ds := e.deployments(t, web.ID)
	if len(ds) != 1 || ds[0].Status != "queued" || ds[0].Trigger != "webhook" || ds[0].CommitSHA != "abc123" || ds[0].CommitMessage != "ship it" {
		t.Fatalf("deployments: %+v", ds)
	}
	// The repository name is matched without regard to case.
	if got := e.deliver("push", "d2", push("Habib/Site", "refs/heads/main", "def456", "x.go")); got != "queued" {
		t.Errorf("mixed-case repository: %q", got)
	}
}

func TestPushesThatShouldNotDeployAreAnsweredWhy(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	web := e.newApp(t, "web")
	e.connectRepo("web", "9:habib/site", "")

	cases := []struct{ name, body, want string }{
		{"another branch", push("habib/site", "refs/heads/feature", "a1", "x.go"), "ignored: not the deploy branch"},
		{"another repository", push("habib/other", "refs/heads/main", "a2", "x.go"), "ignored: no app uses this repository"},
		{"a deleted branch", push("habib/site", "refs/heads/main", "0000000000000000000000000000000000000000"), "ignored: branch deleted"},
		{"a tag", push("habib/site", "refs/tags/v1", "a3", "x.go"), "ignored: not the deploy branch"},
	}
	for i, c := range cases {
		if got := e.deliver("push", "x"+strconv.Itoa(i), c.body); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if n := len(e.deployments(t, web.ID)); n != 0 {
		t.Errorf("%d deployments were queued by pushes that should not deploy", n)
	}
	// A deploy-key source for the same repository name is not touched by the app webhook.
	_, other := e.addApp(t, "keyed", "main")
	if got := e.deliver("push", "k1", push("habib/site", "refs/heads/main", "k", "x.go")); got != "queued" {
		t.Fatalf("push: %q", got)
	}
	if n := len(e.deployments(t, other.ID)); n != 0 {
		t.Error("the GitHub App webhook queued a deploy for a deploy-key app")
	}
}

func TestTheAppWebhookFiltersByBuildFolderAndBranchPerApp(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	site := e.newApp(t, "site")
	api := e.newApp(t, "api")
	e.connectRepo("site", "9:habib/site", "main")
	e.connectRepo("api", "9:habib/site", "feature/x")
	if err := e.st.UpdateAppRootDirectory(site.ID, "site"); err != nil {
		t.Fatal(err)
	}

	// Only the Go code changed on main: the site app (folder "site") is skipped.
	if got := e.deliver("push", "f1", push("habib/site", "refs/heads/main", "s1", "internal/x.go")); got != "ignored: no changes in the build folder" {
		t.Errorf("unrelated files: %q", got)
	}
	if got := e.deliver("push", "f2", push("habib/site", "refs/heads/main", "s2", "site/index.html")); got != "queued" {
		t.Errorf("the build folder changed: %q", got)
	}
	// The api app tracks another branch, so a push to main never reaches it.
	if n := len(e.deployments(t, api.ID)); n != 0 {
		t.Errorf("api (branch feature/x) got %d deployments from a push to main", n)
	}
	if got := e.deliver("push", "f3", push("habib/site", "refs/heads/feature/x", "s3", "anything")); got != "queued" {
		t.Errorf("push to the api app's branch: %q", got)
	}
	if n := len(e.deployments(t, api.ID)); n != 1 {
		t.Errorf("api deployments = %d, want 1", n)
	}
	if n := len(e.deployments(t, site.ID)); n != 1 {
		t.Errorf("site deployments = %d, want 1 (only the push that touched site/)", n)
	}
}

func TestTheSameDeliveryIsHandledOnce(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	a := e.newApp(t, "a")
	b := e.newApp(t, "b")
	e.connectRepo("a", "9:habib/site", "")
	e.connectRepo("b", "9:habib/site", "")

	body := push("habib/site", "refs/heads/main", "same", "x.go")
	if got := e.deliver("push", "dup", body); got != "queued" {
		t.Fatalf("first: %q", got)
	}
	if got := e.deliver("push", "dup", body); got != "duplicate delivery ignored" {
		t.Errorf("replay: %q", got)
	}
	if len(e.deployments(t, a.ID)) != 1 || len(e.deployments(t, b.ID)) != 1 {
		t.Error("two apps on one repository must each deploy exactly once for one delivery")
	}
	// A new delivery id is a new push.
	if got := e.deliver("push", "dup2", body); got != "queued" {
		t.Errorf("new delivery: %q", got)
	}
}

func TestAPrebuiltAppWaitsForItsCIRunNotThePush(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	app := e.newApp(t, "jar")
	e.connectRepo("jar", "9:habib/site", "")
	if err := e.st.UpdateAppDeployMode(app.ID, store.DeployModeArtifact, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := e.deliver("push", "p1", push("habib/site", "refs/heads/main", "z", "x.go")); got != "ignored: this app deploys from CI runs, not pushes" {
		t.Errorf("prebuilt: %q", got)
	}
	if n := len(e.deployments(t, app.ID)); n != 0 {
		t.Error("a prebuilt app was deployed on push")
	}
}

func TestInstallationChangesRefreshTheRepositoryList(t *testing.T) {
	e := newGHEnv(t)
	e.connect(t, "dm.example.com")
	e.newApp(t, "web")
	e.req(http.MethodGet, "/apps/web", "dm.example.com", nil) // fills the picker's cache
	e.s.repoMu.Lock()
	filled := !e.s.repoCacheV.at.IsZero()
	e.s.repoMu.Unlock()
	if !filled {
		t.Fatal("the picker should have cached the repository list")
	}
	for _, ev := range []string{"installation", "installation_repositories"} {
		e.req(http.MethodGet, "/apps/web", "dm.example.com", nil)
		if got := e.deliver(ev, "i-"+ev, `{"action":"added"}`); got != "ok" {
			t.Errorf("%s: %q", ev, got)
		}
		e.s.repoMu.Lock()
		cleared := e.s.repoCacheV.at.IsZero()
		e.s.repoMu.Unlock()
		if !cleared {
			t.Errorf("%s did not clear the cache", ev)
		}
	}
	if got := e.deliver("issues", "i3", `{}`); got != "ignored: not a push event" {
		t.Errorf("other events: %q", got)
	}
}
