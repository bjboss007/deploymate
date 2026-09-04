package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/backup"
	"github.com/habibmuhammad/deploymate/internal/config"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// newBackupTestServer seeds an owner + session + a postgres service, plus a
// real backup.Manager over a temp local destination named "default" (and a
// second "dev" one), and returns the Server + store + service.
func newBackupTestServer(t *testing.T) (*Server, *store.Store, store.Service) {
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
	svc, err := st.CreateService(store.Service{
		ProjectID: proj.ID, Type: "postgres", Name: "PG", Slug: "pg",
		Image: "postgres:16-alpine", Status: "running", VolumeName: "dm-svc-pg-data", Port: 5432,
	})
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	// Credentials so an async "back up now" can reach the exec stage.
	creds := map[string]string{}
	for k, v := range map[string]string{"user": "dm", "password": "pw", "db": "app"} {
		enc, err := crypto.Encrypt([32]byte{}, v)
		if err != nil {
			t.Fatal(err)
		}
		creds[k] = enc
	}
	if err := st.SetServiceCredentials(svc.ID, creds); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	rt := &fakeRuntime{}
	dests, err := backup.NewDestinations(map[string]config.BackupDestination{
		"default": {ID: "default", Type: "local", Dir: t.TempDir()},
		"dev":     {ID: "dev", Type: "local", Dir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	mgr := backup.NewManager(st, rt, [32]byte{}, alerts.New(st, [32]byte{}), dests)
	return &Server{store: st, backups: mgr, encKey: [32]byte{}}, st, svc
}

// postBackupForm posts the form through the real router with the seeded
// session and returns the recorder + flash message (Location query).
func postBackupForm(t *testing.T, s *Server, path string, form url.Values) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST %s status = %d, want 303", path, rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	return rec, loc.Query().Get("flash")
}

func TestBackupConfigSaveEnablesAndGeneratesKey(t *testing.T) {
	s, st, svc := newBackupTestServer(t)

	form := url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"14"}, "destination": {"default"}, "csrf_token": {"csrf"}}
	_, flash := postBackupForm(t, s, "/services/pg/backup", form)
	if !strings.Contains(flash, "enabled") {
		t.Errorf("flash = %q, want an enabled message", flash)
	}

	cfg, err := st.GetBackupConfig(svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.Schedule != "0 2 * * *" || cfg.Keep != 14 || cfg.Destination != "default" {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.KeyEnc == "" {
		t.Fatal("enabling did not generate a backup key")
	}
	// The enable recorded an event.
	evs, _ := st.ListEvents("", 50)
	found := false
	for _, e := range evs {
		if e.Kind == store.EventBackupEnabled {
			found = true
		}
	}
	if !found {
		t.Error("no backup_enabled event")
	}
}

func TestBackupConfigSaveRejects(t *testing.T) {
	s, _, _ := newBackupTestServer(t)
	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{"unknown destination", url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"14"}, "destination": {"nope"}, "csrf_token": {"csrf"}}, "unknown backup destination"},
		{"bad cron", url.Values{"enabled": {"on"}, "schedule": {"nope"}, "keep": {"14"}, "destination": {"default"}, "csrf_token": {"csrf"}}, "invalid schedule"},
		{"bad keep", url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"0"}, "destination": {"default"}, "csrf_token": {"csrf"}}, "keep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, flash := postBackupForm(t, s, "/services/pg/backup", tc.form)
			if !strings.Contains(flash, tc.want) {
				t.Errorf("flash = %q, want containing %q", flash, tc.want)
			}
		})
	}
}

func TestBackupKeyDownloadGate(t *testing.T) {
	s, _, _ := newBackupTestServer(t)

	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "/services/pg/backup/key", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	// Not enabled → 404 (nothing to download).
	if code := get(); code != http.StatusNotFound {
		t.Fatalf("key download while disabled = %d, want 404", code)
	}

	postBackupForm(t, s, "/services/pg/backup", url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"14"}, "destination": {"default"}, "csrf_token": {"csrf"}})

	req := httptest.NewRequest(http.MethodGet, "/services/pg/backup/key", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("key download = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "pg.backup.key") {
		t.Errorf("content disposition = %q", cd)
	}
	if got := strings.TrimSpace(rec.Body.String()); len(got) != 64 {
		t.Errorf("key length = %d, want 64 hex chars", len(got))
	}
}

func TestBackupRestoreRequiresTypedConfirm(t *testing.T) {
	s, st, _ := newBackupTestServer(t)
	postBackupForm(t, s, "/services/pg/backup", url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"14"}, "destination": {"default"}, "csrf_token": {"csrf"}})

	// Wrong typed name → cancelled, nothing started.
	form := url.Values{"object_key": {"backups/pg/20260101T010000Z-aaaaaaaaaaaa.dump.enc"}, "confirm": {"not-the-name"}, "csrf_token": {"csrf"}}
	_, flash := postBackupForm(t, s, "/services/pg/backups/restore", form)
	if !strings.Contains(flash, "Restore cancelled") {
		t.Errorf("flash = %q", flash)
	}
	evs, _ := st.ListEvents("", 50)
	for _, e := range evs {
		if e.Kind == store.EventRestoreStarted {
			t.Fatal("restore started despite confirm mismatch")
		}
	}

	// Typed name but a foreign object key → rejected by the manager.
	form = url.Values{"object_key": {"backups/other/20260101T010000Z-aaaaaaaaaaaa.dump.enc"}, "confirm": {"pg"}, "csrf_token": {"csrf"}}
	_, flash = postBackupForm(t, s, "/services/pg/backups/restore", form)
	if !strings.Contains(flash, "not one of this service's backups") {
		t.Errorf("flash = %q", flash)
	}
}

func TestBackupNowGatesAndStarts(t *testing.T) {
	s, st, _ := newBackupTestServer(t)

	// Disabled → friendly error flash.
	_, flash := postBackupForm(t, s, "/services/pg/backup/now", url.Values{"csrf_token": {"csrf"}})
	if !strings.Contains(flash, "not enabled") {
		t.Errorf("flash = %q, want not-enabled", flash)
	}

	// Enabled → started; the async run lands (the fake container is not
	// running, so the run records a skip — proving the goroutine ran).
	postBackupForm(t, s, "/services/pg/backup", url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"14"}, "destination": {"default"}, "csrf_token": {"csrf"}})
	_, flash = postBackupForm(t, s, "/services/pg/backup/now", url.Values{"csrf_token": {"csrf"}})
	if !strings.Contains(flash, "started") {
		t.Errorf("flash = %q", flash)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := st.ListEvents("", 50)
		for _, e := range evs {
			if e.Kind == store.EventBackupSkipped || e.Kind == store.EventBackupOK {
				return // the async run completed either way
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("async backup run never completed an event")
}

func TestServicePageShowsBackupPanelForPostgresOnly(t *testing.T) {
	s, st, svc := newBackupTestServer(t)
	postBackupForm(t, s, "/services/pg/backup", url.Values{"enabled": {"on"}, "schedule": {"0 2 * * *"}, "keep": {"14"}, "destination": {"default"}, "csrf_token": {"csrf"}})

	owner, _ := st.CreateUser(store.User{Email: "x@test.dev", PasswordHash: "x", Role: "owner"})
	proj2, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateService(store.Service{
		ProjectID: proj2.ID, Type: "mysql", Name: "My", Slug: "my",
		Image: "mysql:8.4", Status: "stopped", VolumeName: "dm-svc-my-data", Port: 3306,
	}); err != nil {
		t.Fatal(err)
	}

	page := func(slug string) string {
		req := httptest.NewRequest(http.MethodGet, "/services/"+slug, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	pgHTML := page(svc.Slug)
	if !strings.Contains(pgHTML, "Database backups") && !strings.Contains(pgHTML, "Backups") {
		t.Error("postgres service page lacks the backups panel")
	}
	myHTML := page("my")
	if strings.Contains(myHTML, "Backup key") {
		t.Error("mysql service page shows the postgres-only backups panel")
	}
}
