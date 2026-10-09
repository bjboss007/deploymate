package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/hostinfo"
	"github.com/habibmuhammad/deploymate/internal/store"
)

type fixedHost struct{ s hostinfo.Snapshot }

func (f fixedHost) Snapshot(context.Context) hostinfo.Snapshot { return f.s }

func busyHost() hostinfo.Snapshot {
	return hostinfo.Snapshot{
		Supported: true, Time: time.Now(), Cores: 4, CPUKnown: true, CPUPct: 37, Load1: 0.5, Load5: 0.7, Load15: 0.9,
		MemTotal: 8 << 30, MemAvailable: 300 << 20, SwapTotal: 2 << 30, SwapUsed: 1 << 30,
		Disks: []hostinfo.Disk{{Label: "Data", Path: "/var/lib/deploymate", Total: 100 << 30, Free: 4 << 30}},
		OS:    "Ubuntu 24.04 LTS", Kernel: "6.8.0", Uptime: 50 * time.Hour,
	}
}

func TestServerPageShowsHostHealth(t *testing.T) {
	e := newWebhookEnv(t)
	e.s.SetHostSource(fixedHost{busyHost()})
	e.s.SetVersion("v0.1.0")
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	if _, err := e.st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	get := func(htmx bool) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/server", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		if htmx {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get(false)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{"Server", "Data", "Ubuntu 24.04 LTS", "v0.1.0", "verdict-crit", "is 96% full", "37%", "Memory"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	if !strings.Contains(body, "<html") {
		t.Error("full page expected")
	}
	// The 15-second refresh asks for the body only.
	_, part := get(true)
	if strings.Contains(part, "<html") || !strings.Contains(part, `id="server-body"`) {
		t.Errorf("htmx request should get only the body: %.120s", part)
	}
}

func TestServerPageOnANonLinuxHost(t *testing.T) {
	e := newWebhookEnv(t)
	e.s.SetHostSource(fixedHost{hostinfo.Snapshot{Time: time.Now()}})
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	req := httptest.NewRequest(http.MethodGet, "/server", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "read from Linux") {
		t.Errorf("status %d, body missing the Linux note", rec.Code)
	}
}

func TestAPIServer(t *testing.T) {
	e := newPrebuiltEnv(t)
	e.s.SetHostSource(fixedHost{busyHost()})
	tok := e.createToken(t, "claude", "read", "90")

	code, body := e.api(t, http.MethodGet, "/api/v1/server", tok)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	if m["status"] != "critical" || m["host_metrics_available"] != true {
		t.Errorf("status/available: %v", m)
	}
	disks := m["disks"].([]any)
	if len(disks) != 1 || disks[0].(map[string]any)["used_percent"].(float64) < 95 {
		t.Errorf("disks: %v", disks)
	}
	// Numbers only: nothing that maps the box.
	for _, leak := range []string{"/var/lib/deploymate", "Ubuntu", "6.8.0"} {
		if strings.Contains(body, leak) {
			t.Errorf("the API response leaks %q", leak)
		}
	}
	if code, _ := e.api(t, http.MethodGet, "/api/v1/server", ""); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
}
