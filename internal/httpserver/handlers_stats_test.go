package httpserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/runtime"
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
	if strings.Contains(body, "Storage") {
		t.Fatalf("body must not show the storage panel without a runtime")
	}
}

// TestStatsPageDiskPanel proves the storage panel renders a live daemon
// snapshot: totals, tracked vs orphaned volumes, top images, and the
// tracked app-images sum from the store.
func TestStatsPageDiskPanel(t *testing.T) {
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
	// A service owning a volume, plus a kept image with a recorded size.
	if _, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "postgres", Name: "pg", Slug: "pg",
		Image: "postgres:16-alpine", VolumeName: "dm-svc-pg-data", Port: 5432,
	}); err != nil {
		t.Fatalf("create service: %v", err)
	}
	if _, err := st.CreateImage(store.Image{AppID: app.ID, Tag: "deploymate/apps/web:x", DeploymentID: "d", SizeBytes: 3145728}); err != nil {
		t.Fatalf("create image: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	fake := &fakeRuntime{disk: runtime.DiskUsage{
		ImagesBytes: 7340032, ContainersBytes: 65536, VolumesBytes: 123456,
		BuildCacheBytes: 2097152, ReclaimableBytes: 1048576,
		ImageCount: 1, ContainerCount: 2, VolumeCount: 2,
		Images: []runtime.ImageUsage{{Tags: []string{"nginx:alpine"}, Size: 7340032, Shared: 1048576, UsedBy: 1}},
		Volumes: []runtime.VolumeUsage{
			{Name: "dm-svc-pg-data", Size: 123456},
			{Name: "orphan-vol", Size: 99},
		},
	}}
	s := &Server{store: st, rt: fake}

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Storage", "Volumes", "Largest images",
		"nginx:alpine", "dm-svc-pg-data", "orphan-vol",
		"DeployMate service", "not tracked by DeployMate",
		"7.0 MB",       // images total (7340032)
		"120.6 KB",     // dm-svc-pg-data (123456)
		"99 B",         // orphan-vol
		"3.0 MB",       // tracked app images (3145728)
		"2.0 MB",       // build cache (2097152)
		"2 volumes", "1 images",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
}

// TestStatsPageDiskError proves a daemon hiccup renders a warning note
// instead of taking the stats page down.
func TestStatsPageDiskError(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	fake := &fakeRuntime{diskErr: errors.New("daemon down")}
	s := &Server{store: st, rt: fake}

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "docker daemon unreachable") {
		t.Fatalf("body missing the daemon-unreachable note")
	}
	if strings.Contains(body, "orphan-vol") || strings.Contains(body, "Largest images") {
		t.Fatalf("error state must not render tables")
	}
}
