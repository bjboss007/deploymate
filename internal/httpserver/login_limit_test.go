package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestClientIPTrustsOnlyTheProxysOwnEntry(t *testing.T) {
	mk := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	for _, c := range []struct{ name, remote, xff, want string }{
		{"direct client, header ignored", "203.0.113.9:5555", "1.2.3.4", "203.0.113.9"},
		{"behind the proxy: last entry", "127.0.0.1:4000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"forged leading entries are not trusted", "127.0.0.1:4000", "1.1.1.1, 2.2.2.2, 198.51.100.7", "198.51.100.7"},
		{"garbage header falls back to the peer", "127.0.0.1:4000", "not-an-ip", "127.0.0.1"},
		{"no header", "127.0.0.1:4000", "", "127.0.0.1"},
	} {
		if got := clientIP(mk(c.remote, c.xff)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLoginLimiterWindowAndForgiveness(t *testing.T) {
	now := time.Now()
	l := newLoginLimiter()
	l.now = func() time.Time { return now }
	for i := 0; i < loginMaxFailsIP-1; i++ {
		l.fail("1.1.1.1", "owner@x.dev")
	}
	if b, _ := l.blocked("1.1.1.1", "owner@x.dev"); b {
		t.Fatal("blocked before the limit")
	}
	l.fail("1.1.1.1", "owner@x.dev")
	b, wait := l.blocked("1.1.1.1", "owner@x.dev")
	if !b || wait <= 0 || wait > loginWindow {
		t.Fatalf("blocked=%v wait=%v", b, wait)
	}
	// Another address is unaffected, and so is the owner's email on its own (30 is far away).
	if b, _ := l.blocked("2.2.2.2", "owner@x.dev"); b {
		t.Error("an unrelated address was blocked by the email counter far below its limit")
	}
	// The window passes.
	now = now.Add(loginWindow + time.Second)
	if b, _ := l.blocked("1.1.1.1", "owner@x.dev"); b {
		t.Error("still blocked after the window")
	}
	// A success forgives the address.
	for i := 0; i < loginMaxFailsIP; i++ {
		l.fail("3.3.3.3", "a@b.c")
	}
	l.succeed("3.3.3.3")
	if b, _ := l.blocked("3.3.3.3", "other@b.c"); b {
		t.Error("a success should clear the address")
	}
}

func TestLoginIsThrottledAfterRepeatedFailures(t *testing.T) {
	e := newWebhookEnv(t)
	hash, _ := auth.HashPassword("correct horse battery")
	e.st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: hash, Role: "owner"})
	post := func(pw string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"email": {"owner@test.dev"}, "password": {pw}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", "198.51.100.20")
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < loginMaxFailsIP; i++ {
		if rec := post("wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d, want 401", i+1, rec.Code)
		}
	}
	rec := post("correct horse battery") // even the right password is refused while blocked
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("after the limit: %d (Retry-After %q), want 429", rec.Code, rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "Too many failed sign-ins") {
		t.Error("the page should say why")
	}
	// A different client address can still sign in.
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(url.Values{"email": {"owner@test.dev"}, "password": {"correct horse battery"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "198.51.100.21")
	rec2 := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusSeeOther {
		t.Errorf("another address: %d, want a redirect (signed in)", rec2.Code)
	}
}
