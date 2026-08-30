package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// TestPreviewProxyBaseTag proves SPAs served under /preview/{slug} get a
// <base> tag so absolute asset URLs resolve to the app instead of the
// dashboard root (where they 404), and that the prefix is stripped on
// the way in. Root-path (subdomain-style) requests must not get a base.
func TestPreviewProxyBaseTag(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(store.App{
		ProjectID: proj.ID, Name: "SPA App", Slug: "spa-app",
		Status: "running", Port: 3000, Image: "example.com/spa:1",
	})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Backend on the app's deterministic preview port, serving a
	// vite-like SPA with absolute asset URLs.
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<!doctype html><html><head><title>spa</title></head><body><script type=\"module\" src=\"/assets/app.js\"></script></body></html>")
		case "/assets/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, "console.log('spa loaded')")
		case "/api/ping":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"pong":"pong"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", runtime.PreviewPort(app.Slug)))
	if err != nil {
		t.Skipf("preview port %d busy: %v", runtime.PreviewPort(app.Slug), err)
	}
	backend.Listener = l
	backend.Start()
	defer backend.Close()

	s := &Server{store: st}

	// Page through the preview prefix: base tag injected.
	rec := httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/preview/spa-app", nil), app)
	if rec.Code != http.StatusOK {
		t.Fatalf("page status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `<base href="/preview/spa-app/">`) {
		t.Errorf("page missing base tag:\n%s", body)
	}

	// Asset through the prefix: prefix stripped, app serves it.
	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/preview/spa-app/assets/app.js", nil), app)
	if rec.Code != http.StatusOK || rec.Body.String() != "console.log('spa loaded')" {
		t.Errorf("asset via prefix: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// API through the prefix.
	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/preview/spa-app/api/ping", nil), app)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pong") {
		t.Errorf("api via prefix: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// Root-path (subdomain-style) request must NOT get a base tag.
	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/", nil), app)
	if body := rec.Body.String(); strings.Contains(body, "<base ") {
		t.Errorf("root-path page must not get a base tag:\n%s", body)
	}
}
