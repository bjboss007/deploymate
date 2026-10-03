package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestAttentionFor(t *testing.T) {
	failed := &store.Deployment{Status: "failed", Error: "swap: staged container failed readiness probe: EOF"}
	ok := &store.Deployment{Status: "running"}
	for _, tc := range []struct {
		name      string
		app       store.App
		last      *store.Deployment
		level     string
		reasonHas string
	}{
		{"healthy", store.App{Status: "running", Health: "healthy"}, ok, "", ""},
		{"stopped is not an alarm", store.App{Status: "stopped"}, nil, "", ""},
		{"failed with a failed deploy", store.App{Status: "failed"}, failed, "bad", "readiness probe"},
		{"failed, container gone", store.App{Status: "failed"}, ok, "bad", "Not running"},
		{"unhealthy", store.App{Status: "running", Health: "unhealthy"}, ok, "bad", "health check"},
		{"deploy failed, old version serving", store.App{Status: "running", Health: "healthy"}, failed, "warn", "previous version is still serving"},
	} {
		level, reason := attentionFor(tc.app, tc.last)
		if level != tc.level || !strings.Contains(reason, tc.reasonHas) {
			t.Errorf("%s: got (%q, %q), want level %q containing %q", tc.name, level, reason, tc.level, tc.reasonHas)
		}
	}
}

// TestFleetBoard: the projects page leads with what needs attention (with
// the real failure reason), summarises the fleet in one line, and draws a
// 24-cell heartbeat per app; the project page keeps create forms out of the
// way behind disclosure buttons.
func TestFleetBoard(t *testing.T) {
	st, good := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Health: "healthy", Port: 8080})
	bad, err := st.CreateApp(store.App{ProjectID: good.ProjectID, Name: "Erp", Slug: "erp", Status: "failed", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := st.CreateApp(store.App{ProjectID: good.ProjectID, Name: "Idle", Slug: "idle", Status: "stopped", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	_ = idle
	if _, err := st.CreateDeployment(store.Deployment{AppID: bad.ID, Kind: "deploy", Status: "failed", Error: "readiness probe EOF: the app exited"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(store.Deployment{AppID: good.ID, Kind: "manual", Status: "running", ImageTag: "nginx:1.27"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}

	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	board := get("/projects")
	for _, want := range []string{
		"Needs attention", "readiness probe EOF: the app exited", // the real reason, up front
		"1 app healthy", "1 need attention", "1 stopped", // the one-line summary
		"nginx:1.27", "Never deployed",
	} {
		if !strings.Contains(board, want) {
			t.Errorf("fleet board lacks %q", want)
		}
	}
	if n := strings.Count(board, `class="strip"`); n != 3 { // each app once: the failing one only under "Needs attention"
		t.Errorf("strips rendered = %d, want 3", n)
	}
	if !strings.Contains(board, "listed under Needs attention") || !strings.Contains(board, "in Test") {
		t.Error("the project list should point at the attention list, and attention rows should name their project")
	}
	if n := strings.Count(board, `class="hb `); n < 24*3 {
		t.Errorf("heartbeat cells = %d, want 24 per strip", n)
	}

	page := get("/projects/test")
	for _, want := range []string{"New app", "New database or cache", "Delete project", "readiness probe EOF"} {
		if !strings.Contains(page, want) {
			t.Errorf("project page lacks %q", want)
		}
	}
	// Create forms live inside <details> — not the first thing on the page.
	if strings.Index(page, "<details") < 0 || strings.Index(page, `name="name"`) < strings.Index(page, "<details") {
		t.Error("create forms must sit behind a disclosure button")
	}
}
