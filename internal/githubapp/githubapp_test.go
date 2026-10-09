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
