package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestDashboardSendsSecurityHeadersButNotOnPreview(t *testing.T) {
	e := newWebhookEnv(t)
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	h := rec.Header()
	if !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'none'") || h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", h)
	}
	if h.Get("Strict-Transport-Security") != "" {
		t.Error("no HSTS over plain HTTP")
	}
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.RemoteAddr, req.Header = "127.0.0.1:1", http.Header{"X-Forwarded-Proto": {"https"}}
	rec = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("HSTS expected behind HTTPS")
	}
	rec = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/preview/nope", nil))
	if rec.Header().Get("Content-Security-Policy") != "" || rec.Header().Get("X-Frame-Options") != "" {
		t.Error("a proxied app keeps its own headers")
	}
}

func TestFailedSignInsAreAuditedAndTheBlockRaisesOneAlert(t *testing.T) {
	e := newWebhookEnv(t)
	hash, _ := auth.HashPassword("right")
	e.st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: hash, Role: "owner"})
	var mu sync.Mutex
	var events []string
	done := make(chan struct{}, 4)
	e.s.SetNotifier(func(ev, subject, _ string) {
		mu.Lock()
		events = append(events, ev+":"+subject)
		mu.Unlock()
		done <- struct{}{}
	})
	post := func(pw string) {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"email": {"owner@test.dev"}, "password": {pw}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		e.s.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}
	for i := 0; i < loginMaxFailsIP+3; i++ {
		post("wrong")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("no alert for the blocked address")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || !strings.HasPrefix(events[0], alerts.EventLoginBlocked+":sign-ins blocked for 203.0.113.9") {
		t.Fatalf("alerts: %v (want exactly one)", events)
	}
	log, _ := e.st.ListAudit(50)
	refused := 0
	for _, a := range log {
		if a.Action == "login" && a.Result == "refused" && a.Detail == "203.0.113.9" {
			refused++
		}
	}
	if refused != loginMaxFailsIP {
		t.Errorf("audited %d refused sign-ins, want %d (blocked attempts are not recorded)", refused, loginMaxFailsIP)
	}
}

func TestLogoutAllEndsEverySessionOfTheUser(t *testing.T) {
	e := newWebhookEnv(t)
	u, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	exp := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	e.st.CreateSession(store.Session{UserID: u.ID, TokenHash: auth.HashToken("a"), CSRFToken: "c", ExpiresAt: exp})
	e.st.CreateSession(store.Session{UserID: u.ID, TokenHash: auth.HashToken("b"), CSRFToken: "c", ExpiresAt: exp})
	req := httptest.NewRequest(http.MethodPost, "/logout-all", strings.NewReader("csrf_token=c"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "a"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("%d", rec.Code)
	}
	for _, tok := range []string{"a", "b"} {
		if _, err := e.st.GetSessionByTokenHash(auth.HashToken(tok)); err == nil {
			t.Errorf("session %s survived", tok)
		}
	}
}

func TestClientAddrTrustsCloudflareOnlyBehindTheTunnel(t *testing.T) {
	e := newWebhookEnv(t)
	mk := func(remote string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("CF-Connecting-IP", "198.51.100.7")
		return r
	}
	if got := e.s.clientAddr(mk("127.0.0.1:1")); got == "198.51.100.7" {
		t.Error("without tunnel mode the header is a client's claim")
	}
	e.s.tunnelMode = true
	if got := e.s.clientAddr(mk("127.0.0.1:1")); got != "198.51.100.7" {
		t.Errorf("tunnel mode, loopback peer: %s", got)
	}
	if got := e.s.clientAddr(mk("192.0.2.5:1")); got == "198.51.100.7" {
		t.Error("a non-loopback peer must never choose its own address")
	}
}

func TestSessionsLastAWeek(t *testing.T) {
	if auth.SessionTTL != 7*24*time.Hour {
		t.Fatal(auth.SessionTTL)
	}
}
