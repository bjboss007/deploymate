package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestNormalizeHealthPath(t *testing.T) {
	good := map[string]string{
		"":                     "/",
		"  ":                   "/",
		"/":                    "/",
		"/healthz":             "/healthz",
		" /api/health ":        "/api/health",
		"/ready?full=1":        "/ready?full=1",
		"/v1/status/ping.json": "/v1/status/ping.json",
	}
	for in, want := range good {
		got, err := normalizeHealthPath(in)
		if err != nil || got != want {
			t.Errorf("normalizeHealthPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"healthz",                 // not origin-relative
		"http://evil.test/health", // a URL, not a path
		"//evil.test/health",      // protocol-relative host
		"/has space",
		"/tab\there",
		"/frag#x",
		"/" + strings.Repeat("a", maxHealthPathLen),
	} {
		if got, err := normalizeHealthPath(bad); err == nil {
			t.Errorf("normalizeHealthPath(%q) = %q, want an error", bad, got)
		}
	}
}

func postHealthPath(t *testing.T, s *Server, slug, path string) string {
	t.Helper()
	form := url.Values{"health_path": {path}, "csrf_token": {"csrf"}}
	req := httptest.NewRequest(http.MethodPost, "/apps/"+slug+"/health-path", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST health-path %q: status %d", path, rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	return loc.Query().Get("flash")
}

// TestHealthPathHandler: an invalid path is rejected without saving; a
// valid one is saved (+ event) and checked on every replica right away —
// the flash names the replicas that would fail Traefik's 2xx/3xx rule.
func TestHealthPathHandler(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Api", Slug: "api", Status: "running", Port: 8080})
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {})
	good := httptest.NewServer(mux)
	t.Cleanup(good.Close)
	notFound := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(notFound.Close)
	port := func(srv *httptest.Server) int {
		u, _ := url.Parse(srv.URL)
		p, _ := strconv.Atoi(u.Port())
		return p
	}
	setSlots(t, st, app, []int{port(good), port(notFound)}, []string{"", ""})
	s := &Server{store: st}

	if flash := postHealthPath(t, s, "api", "healthz"); !strings.Contains(flash, "start with /") {
		t.Errorf("invalid path flash = %q", flash)
	}
	if a, _ := st.GetAppByID(app.ID); a.HealthPath != "/" {
		t.Fatalf("invalid path was saved: %q", a.HealthPath)
	}

	flash := postHealthPath(t, s, "api", "/healthz")
	if a, _ := st.GetAppByID(app.ID); a.HealthPath != "/healthz" {
		t.Fatalf("health path = %q, want /healthz", a.HealthPath)
	}
	if !strings.Contains(flash, "failed on r2 404") || strings.Contains(flash, "failed on r1") {
		t.Errorf("flash must flag r2 (404) and not r1: %q", flash)
	}
	evs, _ := st.ListEvents(app.ID, 5)
	found := false
	for _, e := range evs {
		if e.Kind == store.EventHealthPathChanged && strings.Contains(e.Data, "/ → /healthz") {
			found = true
		}
	}
	if !found {
		t.Errorf("no health_path_changed event: %+v", evs)
	}

	// All replicas healthy → the success message.
	setSlots(t, st, app, []int{port(good)}, []string{""})
	if flash := postHealthPath(t, s, "api", "/healthz"); !strings.Contains(flash, "r1 200") || strings.Contains(flash, "failed") {
		t.Errorf("success flash = %q", flash)
	}

	// Empty resets to "/"; a stopped app skips the probe.
	if err := st.UpdateAppStatus(app.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	if flash := postHealthPath(t, s, "api", ""); !strings.Contains(flash, "checked once the app runs") {
		t.Errorf("stopped-app flash = %q", flash)
	}
	if a, _ := st.GetAppByID(app.ID); a.HealthPath != "/" {
		t.Errorf("empty path must reset to /, got %q", a.HealthPath)
	}
}
