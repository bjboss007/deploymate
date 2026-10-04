package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return m
}

// The read endpoints describe the fleet, an app, its deployments and logs —
// and no response contains a secret value, a connection string or a token.
func TestAPIReadEndpointsAndNoSecrets(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "claude", "read", "90")

	// Seed things that must NOT leak: a secret variable, a normal variable, the
	// repo's webhook secret / deploy key, and the GitHub token.
	for _, v := range []struct {
		k, val string
		secret bool
	}{{"API_TOKEN", "sup3r-s3cret-value", true}, {"LOG_LEVEL", "debug-level-value", false}} {
		enc, _ := crypto.Encrypt(e.s.encKey, v.val)
		e.st.UpsertEnvVar(store.EnvVar{AppID: e.app.ID, Key: v.k, ValueEnc: enc, IsSecret: v.secret})
	}
	e.enablePrebuilt(t) // stores testToken as the GitHub token
	failed, _ := e.st.CreateDeployment(store.Deployment{
		AppID: e.app.ID, Kind: "deploy", Status: "failed", CommitSHA: "abcdef0123456789", CommitMessage: "ship it",
		Error: "readiness probe: EOF",
	})
	e.st.AppendBuildLog(failed.ID, "system", "cloning")
	e.st.AppendBuildLog(failed.ID, "stdout", "BUILD FAILED")
	svc, _ := e.st.CreateService(store.Service{ProjectID: e.app.ProjectID, Type: "redis", Name: "dev-redis", Slug: "dev-redis", Image: "redis:7", Status: "running", Environment: "dev", Port: 6379})
	_ = svc

	var all strings.Builder
	get := func(path string) map[string]any {
		code, body := e.api(t, http.MethodGet, path, tok)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, code, body)
		}
		all.WriteString(body)
		return decode(t, body)
	}

	fleet := get("/api/v1/fleet")
	if sm := fleet["summary"].(map[string]any); sm["apps"].(float64) != 1 {
		t.Errorf("fleet summary = %v", sm)
	}
	if got := get("/api/v1/projects")["projects"].([]any); len(got) != 1 {
		t.Errorf("projects = %v", got)
	}
	proj := get("/api/v1/projects/test")
	if len(proj["apps"].([]any)) != 1 || len(proj["services"].([]any)) != 1 {
		t.Errorf("project = %v", proj)
	}
	app := get("/api/v1/apps/api")
	vars := app["variables"].([]any)
	if len(vars) != 2 {
		t.Fatalf("variables = %v", vars)
	}
	for _, v := range vars {
		m := v.(map[string]any)
		if _, hasValue := m["value"]; hasValue {
			t.Errorf("a variable response carries a value: %v", m)
		}
	}
	if app["explanation"] == nil {
		t.Error("an app whose last deploy failed should carry the explanation")
	}
	deps := get("/api/v1/apps/api/deployments")["deployments"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["status"] != "failed" {
		t.Errorf("deployments = %v", deps)
	}
	dep := get("/api/v1/deployments/" + failed.ID)
	if dep["can_retry"] != true || dep["explanation"] == nil {
		t.Errorf("deployment = %v", dep)
	}
	log := get("/api/v1/deployments/" + failed.ID + "/log?tail=1")
	if lines := log["lines"].([]any); len(lines) != 1 || lines[0] != "BUILD FAILED" || log["truncated"] != true {
		t.Errorf("log = %v", log)
	}
	get("/api/v1/apps/api/activity")
	if svcJSON := get("/api/v1/services/dev-redis"); svcJSON["type"] != "redis" {
		t.Errorf("service = %v", svcJSON)
	}

	// 404s are JSON.
	for _, p := range []string{"/api/v1/apps/nope", "/api/v1/projects/nope", "/api/v1/services/nope", "/api/v1/deployments/nope"} {
		if code, body := e.api(t, http.MethodGet, p, tok); code != http.StatusNotFound || !strings.Contains(body, `"error"`) {
			t.Errorf("%s = %d %s", p, code, body)
		}
	}

	// The whole of what was returned contains none of the secrets.
	text := all.String()
	for _, secret := range []string{"sup3r-s3cret-value", "debug-level-value", testToken, "BEGIN", "whsec"} {
		if strings.Contains(text, secret) {
			t.Errorf("an API response leaked %q", secret)
		}
	}
	if strings.Contains(text, "redis://") || strings.Contains(text, "postgres://") {
		t.Error("an API response carries a connection string")
	}
}
