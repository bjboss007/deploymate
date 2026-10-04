package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
)

// send makes an authenticated JSON request and returns status + decoded body.
func (e *prebuiltEnv) send(t *testing.T, method, path, token string, body any) (int, map[string]any, string) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	var m map[string]any
	if strings.HasPrefix(strings.TrimSpace(rec.Body.String()), "{") {
		m = decode(t, rec.Body.String())
	}
	return rec.Code, m, rec.Body.String()
}

func TestProvisionTierNeedsAProvisionToken(t *testing.T) {
	e := newPrebuiltEnv(t)
	deployer := e.createToken(t, "deployer", "deploy", "90")
	reader := e.createToken(t, "reader", "read", "90")
	for _, tok := range []string{deployer, reader} {
		for _, c := range []struct{ method, path string }{
			{http.MethodPost, "/api/v1/projects"},
			{http.MethodPost, "/api/v1/projects/test/apps"},
			{http.MethodPut, "/api/v1/apps/api/variables"},
			{http.MethodPost, "/api/v1/apps/api/domains"},
		} {
			if code, _, _ := e.send(t, c.method, c.path, tok, map[string]any{"name": "x"}); code != http.StatusForbidden {
				t.Errorf("%s %s with a non-provision token = %d, want 403", c.method, c.path, code)
			}
		}
	}
	if ps, _ := e.st.ListProjects(mustOwnerID(t, e)); len(ps) != 1 {
		t.Errorf("a refused call created a project (%d)", len(ps))
	}
}

func TestProvisionCreatesAndRefuses(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")

	code, res, _ := e.send(t, http.MethodPost, "/api/v1/projects", tok, map[string]string{"name": "Billing Platform"})
	if code != http.StatusCreated || res["slug"] != "billing-platform" {
		t.Fatalf("create project = %d %v", code, res)
	}
	if code, res, _ := e.send(t, http.MethodPost, "/api/v1/projects", tok, map[string]string{"name": "Billing Platform"}); code != http.StatusConflict {
		t.Errorf("duplicate project = %d %v", code, res)
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/projects", tok, map[string]string{"name": ""}); code != http.StatusConflict {
		t.Errorf("empty project name = %d", code)
	}

	code, res, _ = e.send(t, http.MethodPost, "/api/v1/projects/billing-platform/apps", tok, map[string]string{"name": "Ledger", "environment": "staging"})
	if code != http.StatusCreated || res["slug"] != "ledger" || res["environment"] != "staging" {
		t.Fatalf("create app = %d %v", code, res)
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/projects/billing-platform/apps", tok, map[string]string{"name": "Ledger"}); code != http.StatusConflict {
		t.Errorf("duplicate app = %d", code)
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/projects/billing-platform/apps", tok, map[string]string{"name": "Other", "environment": "qa"}); code != http.StatusConflict {
		t.Errorf("unknown environment = %d", code)
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/projects/nope/apps", tok, map[string]string{"name": "X"}); code != http.StatusNotFound {
		t.Errorf("app in a missing project = %d", code)
	}

	code, res, _ = e.send(t, http.MethodPost, "/api/v1/projects/billing-platform/services", tok, map[string]string{"name": "ledger-db", "type": "postgres", "environment": "staging"})
	if code != http.StatusCreated || res["environment"] != "staging" || res["status"] != "stopped" {
		t.Fatalf("create service = %d %v", code, res)
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/projects/billing-platform/services", tok, map[string]string{"name": "x", "type": "oracle"}); code != http.StatusConflict {
		t.Errorf("unknown service type = %d", code)
	}

	// The audit trail names every accepted and refused call.
	entries, _ := e.st.ListAudit(50)
	if len(entries) < 7 {
		t.Errorf("audit has %d entries: %+v", len(entries), entries)
	}
}

// Variables are write-only: stored encrypted, listed by name, never returned,
// and absent from the audit log and the app's history.
func TestProvisionVariablesAreWriteOnly(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")

	code, res, _ := e.send(t, http.MethodPut, "/api/v1/apps/api/variables", tok, map[string]any{
		"variables": map[string]string{"DB_HOST": "db.internal-host-xyz", "DB_PASSWORD": "pw-9f8e7d6c", "FEATURE": "on"},
		"secret":    []string{"FEATURE"},
	})
	if code != http.StatusOK || res["redeploy_needed"] != true || len(res["set"].([]any)) != 3 {
		t.Fatalf("set = %d %v", code, res)
	}
	vars, _ := e.st.ListEnvVars(e.app.ID)
	secrets := map[string]bool{}
	for _, v := range vars {
		secrets[v.Key] = v.IsSecret
		if v.Key == "DB_PASSWORD" {
			if plain, _ := crypto.Decrypt(e.s.encKey, v.ValueEnc); plain != "pw-9f8e7d6c" {
				t.Errorf("stored value does not round-trip: %q", plain)
			}
			if strings.Contains(v.ValueEnc, "pw-9f8e7d6c") {
				t.Error("the value is stored in plaintext")
			}
		}
	}
	if !secrets["DB_PASSWORD"] || !secrets["FEATURE"] || secrets["DB_HOST"] {
		t.Errorf("masking = %v (password-looking and explicitly listed names are masked)", secrets)
	}

	// Nothing the API says afterwards contains a value.
	_, _, appBody := e.send(t, http.MethodGet, "/api/v1/apps/api", tok, nil)
	_, _, actBody := e.send(t, http.MethodGet, "/api/v1/apps/api/activity", tok, nil)
	entries, _ := e.st.ListAudit(10)
	audit, _ := json.Marshal(entries)
	for _, text := range []string{appBody, actBody, string(audit)} {
		for _, secret := range []string{"pw-9f8e7d6c", "db.internal-host-xyz"} {
			if strings.Contains(text, secret) {
				t.Errorf("a value leaked: %q in %.120s", secret, text)
			}
		}
	}
	if !strings.Contains(string(audit), "DB_PASSWORD") {
		t.Error("the audit log should name the variables that were set")
	}

	// All or nothing: one bad name rejects the call and writes nothing.
	code, res, _ = e.send(t, http.MethodPut, "/api/v1/apps/api/variables", tok, map[string]any{
		"variables": map[string]string{"GOOD_ONE": "1", "bad name": "2"},
	})
	if code != http.StatusConflict {
		t.Fatalf("invalid name = %d %v", code, res)
	}
	for _, v := range func() []string {
		var ks []string
		vs, _ := e.st.ListEnvVars(e.app.ID)
		for _, v := range vs {
			ks = append(ks, v.Key)
		}
		return ks
	}() {
		if v == "GOOD_ONE" {
			t.Error("a rejected call still saved a variable")
		}
	}
	if code, _, _ := e.send(t, http.MethodPut, "/api/v1/apps/api/variables", tok, map[string]any{"variables": map[string]string{}}); code != http.StatusConflict {
		t.Errorf("empty variables = %d", code)
	}
}

// Connecting a repository hands back only the PUBLIC deploy key — never the
// private key or the webhook secret — and refuses to replace an existing one.
func TestProvisionConnectRepoNeverLeaksSecrets(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")
	e.send(t, http.MethodPost, "/api/v1/projects/test/apps", tok, map[string]string{"name": "Fresh"})

	code, res, body := e.send(t, http.MethodPost, "/api/v1/apps/fresh/git", tok, map[string]string{
		"repository": "git@github.com:acme/fresh.git", "branch": "develop",
	})
	if code != http.StatusCreated || res["branch"] != "develop" || !strings.HasPrefix(res["deploy_key"].(string), "ssh-") {
		t.Fatalf("connect = %d %v", code, res)
	}
	app, _ := e.st.GetAppBySlug("fresh")
	gs, _ := e.st.GetGitSource(app.GitSourceID)
	secret, _ := crypto.Decrypt(e.s.encKey, gs.WebhookSecretEnc)
	if strings.Contains(body, secret) || strings.Contains(body, "PRIVATE KEY") {
		t.Error("the response leaked the webhook secret or the private key")
	}
	if code, res, _ := e.send(t, http.MethodPost, "/api/v1/apps/fresh/git", tok, map[string]string{"repository": "git@github.com:acme/other.git"}); code != http.StatusConflict || !strings.Contains(res["error"].(string), "already") {
		t.Errorf("reconnect = %d %v", code, res)
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/apps/api/git", tok, map[string]string{"repository": "git@github.com:acme/x.git"}); code != http.StatusConflict {
		t.Errorf("an app that already has a repo must refuse (%d)", code)
	}
	e.send(t, http.MethodPost, "/api/v1/projects/test/apps", tok, map[string]string{"name": "Third"})
	for _, bad := range []map[string]string{{"repository": "ftp://x"}, {"repository": "git@github.com:a/b.git", "provider": "bitbucket"}} {
		if code, _, _ := e.send(t, http.MethodPost, "/api/v1/apps/third/git", tok, bad); code != http.StatusConflict {
			t.Errorf("%v = %d", bad, code)
		}
	}
}

func TestProvisionDomainsAndConfig(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/apps/api/domains", tok, map[string]string{"hostname": "Shop.Example.com"}); code != http.StatusCreated {
		t.Errorf("add domain = %d", code)
	}
	if ds, _ := e.st.ListDomains(e.app.ID); len(ds) != 1 || ds[0].Hostname != "shop.example.com" || ds[0].TLSStatus != "pending" {
		t.Errorf("domains = %+v", ds)
	}
	for _, bad := range []string{"not a host", "http://x.com", ""} {
		if code, _, _ := e.send(t, http.MethodPost, "/api/v1/apps/api/domains", tok, map[string]string{"hostname": bad}); code != http.StatusConflict {
			t.Errorf("hostname %q = %d", bad, code)
		}
	}
	if code, _, _ := e.send(t, http.MethodPost, "/api/v1/apps/api/domains", tok, map[string]string{"hostname": "shop.example.com"}); code != http.StatusConflict {
		t.Errorf("duplicate domain = %d", code)
	}

	if code, res, _ := e.send(t, http.MethodPatch, "/api/v1/apps/api/config", tok, map[string]any{"image": "nginx:1.27", "port": 80}); code != http.StatusOK || res["image"] != "nginx:1.27" {
		t.Errorf("configure = %d %v", code, res)
	}
	if app, _ := e.reload(t); app.Image != "nginx:1.27" || app.Port != 80 {
		t.Errorf("app = image %q port %d", app.Image, app.Port)
	}
	for _, bad := range []map[string]any{{"image": "has space"}, {"port": 0}, {"port": 70000}, {}} {
		if code, _, _ := e.send(t, http.MethodPatch, "/api/v1/apps/api/config", tok, bad); code != http.StatusConflict {
			t.Errorf("%v = %d, want 409", bad, code)
		}
	}
}

// There is no way to delete anything through the API, whatever the scope.
func TestAPIHasNoDeletes(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")
	for _, path := range []string{"/api/v1/apps/api", "/api/v1/projects/test", "/api/v1/services/x", "/api/v1/apps/api/variables", "/api/v1/deployments/x"} {
		if code, _, _ := e.send(t, http.MethodDelete, path, tok, nil); code/100 == 2 {
			t.Errorf("DELETE %s succeeded (%d)", path, code)
		}
	}
	if _, err := e.st.GetAppBySlug("api"); err != nil {
		t.Errorf("the app is gone: %v", err)
	}
}
