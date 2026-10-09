package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

func TestManifestAsksForLeastPrivilege(t *testing.T) {
	b, err := Manifest(ManifestOpts{Name: "DeployMate dm.example.com", HomepageURL: "https://dm.example.com",
		RedirectURL: "https://dm.example.com/settings/github/callback", SetupURL: "https://dm.example.com/settings/github/installed",
		WebhookURL: "https://dm.example.com/hooks/github-app"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Public bool `json:"public"`
		Hook   struct {
			URL    string `json:"url"`
			Active bool   `json:"active"`
		} `json:"hook_attributes"`
		Perms  map[string]string `json:"default_permissions"`
		Events []string          `json:"default_events"`
		Redir  string            `json:"redirect_url"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Public {
		t.Error("the app must be private")
	}
	if !m.Hook.Active || m.Hook.URL != "https://dm.example.com/hooks/github-app" {
		t.Errorf("hook: %+v", m.Hook)
	}
	want := map[string]string{"contents": "read", "metadata": "read", "actions": "write"}
	if len(m.Perms) != len(want) {
		t.Errorf("permissions %v, want exactly %v", m.Perms, want)
	}
	for k, v := range want {
		if m.Perms[k] != v {
			t.Errorf("permission %s = %q, want %q", k, m.Perms[k], v)
		}
	}
	if strings.Join(m.Events, ",") != "push,workflow_run" {
		t.Errorf("events = %v", m.Events)
	}
}

func TestManifestWithoutAPublicAddressHasAnInactiveHook(t *testing.T) {
	b, err := Manifest(ManifestOpts{HomepageURL: "http://127.0.0.1:8080", RedirectURL: "http://127.0.0.1:8080/settings/github/callback"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"active":false`) {
		t.Errorf("the webhook must be inactive without a public URL: %s", b)
	}
	if _, err := Manifest(ManifestOpts{}); err == nil {
		t.Error("a manifest without URLs must be refused")
	}
	long := strings.Repeat("x", 80)
	b2, _ := Manifest(ManifestOpts{Name: long, HomepageURL: "https://a", RedirectURL: "https://a/cb"})
	var m struct{ Name string }
	json.Unmarshal(b2, &m)
	if len([]rune(m.Name)) > 34 {
		t.Errorf("name %d chars, GitHub allows 34", len([]rune(m.Name)))
	}
}

func TestNewFormURL(t *testing.T) {
	if got := NewFormURL("", "", "s 1"); got != "https://github.com/settings/apps/new?state=s+1" {
		t.Errorf("personal: %s", got)
	}
	if got := NewFormURL("", "my-org", "abc"); got != "https://github.com/organizations/my-org/settings/apps/new?state=abc" {
		t.Errorf("org: %s", got)
	}
	if got := NewFormURL("", "a/b", "x"); strings.Contains(got, "a/b") {
		t.Errorf("an organisation name must be escaped: %s", got)
	}
	a, _ := NewState()
	b, _ := NewState()
	if a == b || len(a) < 24 {
		t.Error("states must be unguessable and unique")
	}
}

func TestAppJWTRoundTrip(t *testing.T) {
	k, pemKey := newKey(t)
	now := time.Now()
	tok, err := AppJWT(4242, pemKey, now)
	if err != nil {
		t.Fatal(err)
	}
	iss, err := VerifyAppJWT(tok, &k.PublicKey, now)
	if err != nil || iss != 4242 {
		t.Fatalf("verify: %d %v", iss, err)
	}
	if _, err := VerifyAppJWT(tok, &k.PublicKey, now.Add(11*time.Minute)); err == nil {
		t.Error("an expired token was accepted")
	}
	other, _ := newKey(t)
	if _, err := VerifyAppJWT(tok, &other.PublicKey, now); err == nil {
		t.Error("a token signed by another key was accepted")
	}
	// Tampering with the claims breaks the signature.
	parts := strings.Split(tok, ".")
	forged := parts[0] + ".eyJpc3MiOjF9." + parts[2]
	if _, err := VerifyAppJWT(forged, &k.PublicKey, now); err == nil {
		t.Error("forged claims accepted")
	}
}

func TestParseKeyAcceptsPKCS8AndRejectsGarbage(t *testing.T) {
	k, _ := newKey(t)
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	if _, err := ParseKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))); err != nil {
		t.Errorf("pkcs8: %v", err)
	}
	if _, err := ParseKey("not a key"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestConvertManifestAndListInstallations(t *testing.T) {
	k, pemKey := newKey(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/app-manifests/good/conversions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
			t.Errorf("conversion must be an unauthenticated POST (got %s, auth %q)", r.Method, r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 77, "slug": "deploymate-x", "name": "DeployMate x", "pem": pemKey,
			"webhook_secret": "whsec", "client_id": "Iv1.x", "client_secret": "cs", "html_url": "https://github.com/apps/deploymate-x",
			"owner": map[string]string{"login": "habib", "type": "User"}})
	})
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		iss, err := VerifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &k.PublicKey, time.Now())
		if err != nil || iss != 77 {
			w.WriteHeader(401)
			w.Write([]byte(`{"message":"A JSON web token could not be decoded"}`))
			return
		}
		w.Write([]byte(`[{"id":1,"repository_selection":"selected","account":{"login":"habib","type":"User"}}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := New(srv.URL)

	cr, err := c.ConvertManifest(context.Background(), "good")
	if err != nil || cr.ID != 77 || cr.WebhookSecret != "whsec" || cr.PEM == "" {
		t.Fatalf("convert: %+v %v", cr, err)
	}
	if _, err := c.ConvertManifest(context.Background(), "stale"); err == nil {
		t.Error("an unknown code must fail")
	}
	if _, err := c.ConvertManifest(context.Background(), "../x"); err == nil {
		t.Error("a code with path characters must be refused before any request")
	}
	ins, err := c.ListInstallations(context.Background(), 77, pemKey)
	if err != nil || len(ins) != 1 || ins[0].Account.Login != "habib" {
		t.Fatalf("installations: %+v %v", ins, err)
	}
	if _, err := c.ListInstallations(context.Background(), 99, pemKey); err == nil {
		t.Error("a JWT for the wrong app id must be refused by GitHub")
	}
}

func TestTokensAreReusedUntilCloseToExpiry(t *testing.T) {
	_, pemKey := newKey(t)
	calls := 0
	exp := time.Now().Add(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"token": "tok" + string(rune('0'+calls)), "expires_at": exp.UTC().Format(time.RFC3339)})
	}))
	defer srv.Close()
	ts := NewTokens(New(srv.URL))
	now := time.Now()
	ts.now = func() time.Time { return now }

	a, _ := ts.Get(context.Background(), 1, pemKey, 5)
	b, _ := ts.Get(context.Background(), 1, pemKey, 5)
	if a != b || calls != 1 {
		t.Fatalf("reuse: %q %q calls=%d", a, b, calls)
	}
	now = exp.Add(-4 * time.Minute) // inside the refresh margin
	c, _ := ts.Get(context.Background(), 1, pemKey, 5)
	if c == a || calls != 2 {
		t.Errorf("a token about to expire must be replaced (calls=%d)", calls)
	}
	// A different app id never gets another app's cached token.
	if _, _ = ts.Get(context.Background(), 2, pemKey, 5); calls != 3 {
		t.Errorf("another app must not reuse the cache (calls=%d)", calls)
	}
}

func TestBranchExistsAndRepoListing(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/branches/"):
			if strings.HasSuffix(r.URL.Path, "/main") || strings.HasSuffix(r.URL.Path, "/feature/x") {
				w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Branch not found"}`))
		case r.URL.Path == "/installation/repositories":
			w.Write([]byte(`{"repositories":[{"id":1,"full_name":"o/r","clone_url":"https://github.com/o/r.git","default_branch":"main"}]}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	for branch, want := range map[string]bool{"main": true, "feature/x": true, "nope": false} {
		got, err := c.BranchExists(context.Background(), "t", "o/r", branch)
		if err != nil || got != want {
			t.Errorf("BranchExists(%q) = %v, %v; want %v", branch, got, err, want)
		}
	}
	if _, err := c.BranchExists(context.Background(), "t", "../etc", "main"); err == nil {
		t.Error("a repository that is not owner/name must be refused before any request")
	}
	for _, bad := range []string{"../../x", "a/../b", "/lead", "trail/", "a//b", ".."} {
		before := len(paths)
		if _, err := c.BranchExists(context.Background(), "t", "o/r", bad); err == nil || len(paths) != before {
			t.Errorf("branch %q must be refused without any request (err %v)", bad, err)
		}
	}
	repos, err := c.ListInstallationRepos(context.Background(), "t")
	if err != nil || len(repos) != 1 || repos[0].DefaultBranch != "main" {
		t.Errorf("repos: %+v %v", repos, err)
	}
}
