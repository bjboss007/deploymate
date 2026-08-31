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

// seedReleasesApp builds a logged-in server with one app and three
// deployments: one running (current), one failed, one older running with an
// image tag (rollbackable).
func seedReleasesApp(t *testing.T) (*Server, store.App) {
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
	now := time.Now().UTC()
	old, err := st.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "running", Trigger: "webhook",
		CommitSHA: "oldsha", StartedAt: now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
		FinishedAt: now.Add(-9 * time.Minute).Format(time.RFC3339Nano), ImageTag: "deploymate/apps/web:old",
	})
	if err != nil {
		t.Fatalf("create old deployment: %v", err)
	}
	failed, err := st.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "failed", Trigger: "webhook",
		CommitSHA: "badsha", Error: "build failed",
		StartedAt: now.Add(-5 * time.Minute).Format(time.RFC3339Nano),
		FinishedAt: now.Add(-4 * time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("create failed deployment: %v", err)
	}
	cur, err := st.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "running", Trigger: "dashboard",
		CommitSHA: "newsha", CommitMessage: "ship it",
		StartedAt: now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
		FinishedAt: now.Add(-time.Minute).Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("create current deployment: %v", err)
	}
	if err := st.SetAppCurrentDeployment(app.ID, cur.ID); err != nil {
		t.Fatalf("set current: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_ = old
	_ = failed
	return &Server{store: st}, app
}

// getReleases hits the releases page through the real router.
func getReleases(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestReleasesPageShowsAllRows verifies the unfiltered list: every row with
// status badge, trigger, duration, current marker, and rollback only on the
// non-current image-backed row.
func TestReleasesPageShowsAllRows(t *testing.T) {
	s, _ := seedReleasesApp(t)
	rec := getReleases(t, s, "/apps/web/releases")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"webhook", "dashboard", "badge badge-running", "badge badge-failed", "badge-accent", "current", "deploymate/apps/web:old", "newsha", "badsha", "Rollback", "1m0s"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
	// The failed deployment is not rollbackable and neither is the current
	// one: exactly one Rollback form.
	if n := strings.Count(body, ">Rollback</button>"); n != 1 {
		t.Fatalf("rollback buttons = %d, want 1", n)
	}
}

// TestReleasesPageFilters verifies ?status=successful / failed narrowing.
func TestReleasesPageFilters(t *testing.T) {
	s, _ := seedReleasesApp(t)

	rec := getReleases(t, s, "/apps/web/releases?status=successful")
	body := rec.Body.String()
	if !strings.Contains(body, "newsha") || !strings.Contains(body, "deploymate/apps/web:old") {
		t.Fatalf("successful filter missing running rows: %s", body)
	}
	if strings.Contains(body, "badsha") {
		t.Fatalf("successful filter includes failed row")
	}

	rec = getReleases(t, s, "/apps/web/releases?status=failed")
	body = rec.Body.String()
	if !strings.Contains(body, "badsha") {
		t.Fatalf("failed filter missing failed row")
	}
	if strings.Contains(body, "newsha") {
		t.Fatalf("failed filter includes running row")
	}
}
