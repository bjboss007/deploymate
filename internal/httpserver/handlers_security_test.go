package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestSecurityAPIReportsRedisAndExposedContainers(t *testing.T) {
	e := newPrebuiltEnv(t)
	fake := &fakeRuntime{info: runtime.Info{Running: true, State: "running",
		PublicBindings: []string{"0.0.0.0:8080 (8080/tcp)"}, Privileged: false, NoNewPrivileges: false}}
	e.s.rt = fake
	e.s.SetSecurityContext("127.0.0.1:8080", false)
	e.st.CreateService(store.Service{ProjectID: e.app.ProjectID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: "dev", Port: 6379})
	tok := e.createToken(t, "claude", "read", "90")

	code, body := e.api(t, http.MethodGet, "/api/v1/security", tok)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	if m["status"] != "critical" {
		t.Errorf("a published container port is critical, got %v", m["status"])
	}
	ids := map[string]string{}
	for _, c := range m["checks"].([]any) {
		cm := c.(map[string]any)
		ids[cm["id"].(string)] = cm["level"].(string)
	}
	if ids["containers-public"] != "critical" {
		t.Errorf("containers-public: %v", ids)
	}
	if ids["redis"] != "warning" {
		t.Errorf("a Redis without a password must be flagged: %v", ids)
	}
	if ids["dashboard"] != "ok" {
		t.Errorf("a loopback dashboard is fine: %v", ids)
	}
	if code, _ := e.api(t, http.MethodGet, "/api/v1/security", ""); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
}

func TestSecurityPageRendersTheChecks(t *testing.T) {
	e := newWebhookEnv(t)
	e.s.rt = &fakeRuntime{inspectErr: runtime.ErrContainerNotFound}
	e.s.SetSecurityContext("0.0.0.0:8080", true)
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	req := httptest.NewRequest(http.MethodGet, "/security", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"Security", "Dashboard address", "listens on every network interface", "DEPLOYMATE_ADDR=127.0.0.1:8080", "Tunnel mode"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Without a session the page is not served.
	rec2 := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/security", nil))
	if rec2.Code == http.StatusOK {
		t.Error("the security page must need a session")
	}
}
