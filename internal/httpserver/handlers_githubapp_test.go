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
	key    *rsa.PrivateKey
	pemKey string
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
