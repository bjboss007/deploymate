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

// TestPreviewProxyRewritesURLs proves SPAs served under /preview/{slug}
// get their absolute-path src/href URLs rewritten to carry the prefix
// (a <base> tag can't help — absolute paths replace the base's path),
// and that the prefix is stripped on the way in. Protocol-relative,
// already-prefixed, and root-path (subdomain-style) requests are left
// alone.
func TestPreviewProxyRewritesURLs(t *testing.T) {
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
	// vite-like SPA with absolute, relative, and already-prefixed URLs.
	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><html><head><title>spa</title>`+
				`<link href="/assets/style.css">`+
				`<link href="/preview/spa-app/keep.css">`+
				`<a href="//cdn.example.com/x">cdn</a>`+
				`<a href="/about">app route</a>`+
				`</head><body><script type="module" src="/assets/app.js"></script></body></html>`)
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

	// Page through the preview prefix: absolute URLs rewritten, the rest
	// untouched.
	rec := httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/preview/spa-app", nil), app)
	if rec.Code != http.StatusOK {
		t.Fatalf("page status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`src="/preview/spa-app/assets/app.js"`,
		`href="/preview/spa-app/assets/style.css"`,
		`href="/preview/spa-app/about"`,
		`href="/preview/spa-app/keep.css"`, // already prefixed: unchanged
		`href="//cdn.example.com/x"`,        // protocol-relative: unchanged
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `src="/assets/app.js"`) {
		t.Errorf("absolute src not rewritten:\n%s", body)
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

	// Root-path (subdomain-style) request must not be rewritten (the
	// fixture's own already-prefixed keep.css link is expected to remain).
	rec = httptest.NewRecorder()
	s.proxyToApp(rec, httptest.NewRequest("GET", "/", nil), app)
	if body := rec.Body.String(); strings.Contains(body, `src="/preview/spa-app/assets/app.js"`) {
		t.Errorf("root-path page must not be rewritten:\n%s", body)
	}
}
