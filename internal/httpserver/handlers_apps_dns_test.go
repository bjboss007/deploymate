package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/dns"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// fakeDNS records the hosts asked for and can inject a failure, mirroring
// the fakeRuntime pattern (handlers_apps_test.go).
type fakeDNS struct {
	hosts []string
	err   error
}

func (f *fakeDNS) EnsurePreviewRecord(_ context.Context, host string) error {
	f.hosts = append(f.hosts, host)
	return f.err
}

// newDNSAppServer seeds a store with an owner + project + logged-in session
// and POSTs an app creation through the real router. Returns the server
// (for store assertions) and the recorder.
func newDNSAppServer(t *testing.T, creator dns.Creator, previewHost string) (*Server, *httptest.ResponseRecorder) {
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
	if _, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	s := &Server{store: st, dns: creator, previewHost: previewHost}
	form := url.Values{"name": {"My App"}, "csrf_token": {"csrf"}}
	req := httptest.NewRequest(http.MethodPost, "/projects/test/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return s, rec
}

func TestAppCreateCreatesPreviewDNSRecord(t *testing.T) {
	fd := &fakeDNS{}
	_, rec := newDNSAppServer(t, fd, "dm.getmerchanttech.com")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/apps/my-app" {
		t.Errorf("location = %q, want /apps/my-app", loc)
	}
	if len(fd.hosts) != 1 || fd.hosts[0] != "my-app.dm.getmerchanttech.com" {
		t.Errorf("dns called with %v, want [my-app.dm.getmerchanttech.com]", fd.hosts)
	}
}

func TestAppCreateDNSFailureIsNonFatal(t *testing.T) {
	fd := &fakeDNS{err: errors.New("cf down")}
	s, rec := newDNSAppServer(t, fd, "dm.getmerchanttech.com")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (DNS must not fail app creation)", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/apps/my-app") || !strings.Contains(loc, "flash=") {
		t.Errorf("location = %q, want /apps/my-app with a flash", loc)
	}
	app, err := s.store.GetAppBySlug("my-app")
	if err != nil {
		t.Fatalf("app should still be created: %v", err)
	}
	events, err := s.store.ListEvents(app.ID, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Kind == store.EventDNSRecordFailed {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a %s event, events were %+v", store.EventDNSRecordFailed, events)
	}
}

func TestAppCreateWithoutDNSCreator(t *testing.T) {
	s, rec := newDNSAppServer(t, nil, "dm.getmerchanttech.com")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if _, err := s.store.GetAppBySlug("my-app"); err != nil {
		t.Errorf("app should be created without a dns creator: %v", err)
	}
}

func TestAppCreateWithoutPreviewHostSkipsDNS(t *testing.T) {
	fd := &fakeDNS{}
	_, rec := newDNSAppServer(t, fd, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if len(fd.hosts) != 0 {
		t.Errorf("dns called with %v, want none with empty previewHost", fd.hosts)
	}
}
