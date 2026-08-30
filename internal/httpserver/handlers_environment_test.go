package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// postEnvironment seeds an owner + project + logged-in session + app, then
// POSTs the given environment value through the real router and returns the
// server (for store assertions) and the recorder.
func postEnvironment(t *testing.T, env string) (*Server, store.App, *httptest.ResponseRecorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	app, err := st.CreateApp(store.App{ProjectID: proj.ID, Name: "Web", Slug: "web"})
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	s := &Server{store: st}
	form := url.Values{"environment": {env}, "csrf_token": {"csrf"}}
	req := httptest.NewRequest(http.MethodPost, "/apps/web/environment", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return s, app, rec
}

// TestSetEnvironmentAcceptsDev drives the router end-to-end for the dev value.
func TestSetEnvironmentAcceptsDev(t *testing.T) {
	s, _, rec := postEnvironment(t, store.EnvDev)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/apps/web") {
		t.Errorf("location = %q, want /apps/web...", loc)
	}
	got, err := s.store.GetAppBySlug("web")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Environment != store.EnvDev {
		t.Fatalf("app env = %q, want dev", got.Environment)
	}
}

// TestSetEnvironmentRejectsUnknown proves an unknown value is refused and the
// stored environment is left untouched.
func TestSetEnvironmentRejectsUnknown(t *testing.T) {
	s, _, rec := postEnvironment(t, "prod-ish")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (redirect with flash)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "flash=") {
		t.Errorf("location = %q, want a flash error", loc)
	}
	got, err := s.store.GetAppBySlug("web")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if got.Environment != store.EnvDev { // unchanged from the default
		t.Fatalf("app env = %q, want unchanged dev", got.Environment)
	}
}
