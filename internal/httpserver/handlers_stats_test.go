package httpserver

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// TestStatsPageRendersFleetData drives the real router: a logged-in session
// sees the fleet totals and per-app rows.
func TestStatsPageRendersFleetData(t *testing.T) {
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
	if _, err := st.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "running",
		StartedAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
		FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	s := &Server{store: st}

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Build statistics", "success rate", "deploys (30d)", "Web"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
}
