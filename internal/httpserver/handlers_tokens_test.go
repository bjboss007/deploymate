package httpserver

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

var tokenRe = regexp.MustCompile(`dm_[A-Za-z0-9_-]{40,}`)

func (e *prebuiltEnv) api(t *testing.T, method, path, token string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// A logged-in browser session must not matter on /api.
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func (e *prebuiltEnv) createToken(t *testing.T, name, scope, expires string) string {
	t.Helper()
	form := url.Values{"name": {name}, "scope": {scope}, "expires": {expires}, "csrf_token": {"csrf"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/tokens", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create token: %d (%s)", rec.Code, rec.Header().Get("Location"))
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page showing a new token must be no-store")
	}
	m := tokenRe.FindString(rec.Body.String())
	if m == "" {
		t.Fatalf("no token in the page:\n%s", rec.Body.String())
	}
	return m
}

// A created token is shown once, stored only as a hash, works as a bearer on
// /api, and stops working when revoked.
func TestAPITokenLifecycle(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "claude", "read", "90")

	// Stored as a hash: neither the plaintext nor any long slice of it is in the DB.
	var stored string
	e.st.DB().QueryRow(`SELECT token_hash || '|' || prefix FROM api_tokens`).Scan(&stored)
	if strings.Contains(stored, tok[6:]) {
		t.Errorf("plaintext leaked into the database: %q", stored)
	}

	// The list page never shows it again — only the short prefix.
	req := httptest.NewRequest(http.MethodGet, "/settings/tokens", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), tok) || !strings.Contains(rec.Body.String(), tok[:9]) {
		t.Error("the list shows the prefix only, never the whole token")
	}
	if !strings.Contains(rec.Body.String(), "Read-only") || !strings.Contains(rec.Body.String(), "Never used") {
		t.Error("the list should show scope and last use")
	}

	code, body := e.api(t, http.MethodGet, "/api/v1/whoami", tok)
	if code != http.StatusOK || !strings.Contains(body, `"owner@test.dev"`) || !strings.Contains(body, `"scope":"read"`) {
		t.Fatalf("whoami = %d %s", code, body)
	}
	var last string
	e.st.DB().QueryRow(`SELECT last_used_at FROM api_tokens`).Scan(&last)
	if last == "" {
		t.Error("a use should be recorded")
	}

	// Revoke -> dead at once.
	ts, _ := e.st.ListAPITokens(mustOwnerID(t, e))
	if len(ts) != 1 {
		t.Fatalf("tokens = %d", len(ts))
	}
	e.post(t, "/settings/tokens/"+ts[0].ID+"/revoke", url.Values{})
	if code, _ := e.api(t, http.MethodGet, "/api/v1/whoami", tok); code != http.StatusUnauthorized {
		t.Errorf("a revoked token still works (%d)", code)
	}
}

func mustOwnerID(t *testing.T, e *prebuiltEnv) string {
	t.Helper()
	u, err := e.st.GetUserByEmail("owner@test.dev")
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// The API refuses everything that is not a valid bearer token — including a
// perfectly good browser session — and says nothing about why a token failed.
func TestAPIRejectsBadCredentials(t *testing.T) {
	e := newPrebuiltEnv(t)
	good := e.createToken(t, "x", "read", "90")
	for name, tok := range map[string]string{
		"no token":      "",
		"wrong token":   "dm_" + strings.Repeat("A", 43),
		"not dm_":       "abc",
		"truncated":     good[:len(good)-1],
		"a cookie only": "",
	} {
		code, body := e.api(t, http.MethodGet, "/api/v1/whoami", tok)
		if code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, code)
		}
		if strings.Contains(body, "revoked") || strings.Contains(body, "expired token ") {
			t.Errorf("%s: the error should not explain why: %s", name, body)
		}
	}
}

func TestAPITokenExpiry(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "short", "read", "30")
	if code, _ := e.api(t, http.MethodGet, "/api/v1/whoami", tok); code != http.StatusOK {
		t.Fatalf("fresh token: %d", code)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := e.st.DB().Exec(`UPDATE api_tokens SET expires_at = ?`, past); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.api(t, http.MethodGet, "/api/v1/whoami", tok); code != http.StatusUnauthorized {
		t.Errorf("an expired token still works (%d)", code)
	}
}

// A read-only token cannot make a non-GET request; a read & act token can.
// (Checked on the middleware itself with a stand-in handler.)
func TestReadTokenCannotChangeThings(t *testing.T) {
	e := newPrebuiltEnv(t)
	reader := e.createToken(t, "r", "read", "90")
	writer := e.createToken(t, "w", "write", "90")
	am := &auth.Middleware{Store: e.st}
	h := am.RequireAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	do := func(method, tok string) int {
		req := httptest.NewRequest(method, "/api/v1/anything", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do(http.MethodGet, reader); c != http.StatusNoContent {
		t.Errorf("read token GET = %d", c)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if c := do(m, reader); c != http.StatusForbidden {
			t.Errorf("read token %s = %d, want 403", m, c)
		}
		if c := do(m, writer); c != http.StatusNoContent {
			t.Errorf("write token %s = %d, want allowed", m, c)
		}
	}
}

func TestTokenCreateValidationAndCap(t *testing.T) {
	e := newPrebuiltEnv(t)
	for name, form := range map[string]url.Values{
		"no name":    {"name": {""}, "scope": {"read"}, "expires": {"90"}},
		"bad scope":  {"name": {"x"}, "scope": {"root"}, "expires": {"90"}},
		"bad expiry": {"name": {"x"}, "scope": {"read"}, "expires": {"forever"}},
	} {
		code, loc := e.post(t, "/settings/tokens", form)
		if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/settings/tokens?flash=") {
			t.Errorf("%s: %d %q", name, code, loc)
		}
	}
	if n, _ := e.st.CountAPITokens(mustOwnerID(t, e)); n != 0 {
		t.Errorf("invalid requests created %d tokens", n)
	}
	for i := 0; i < maxAPITokens; i++ {
		e.createToken(t, "t", "read", "never")
	}
	_, loc := e.post(t, "/settings/tokens", url.Values{"name": {"one too many"}, "scope": {"read"}, "expires": {"90"}})
	if !strings.Contains(flashOf(t, loc), "20 tokens") {
		t.Errorf("cap flash = %q", flashOf(t, loc))
	}
	// Someone else's token id cannot be revoked by guessing it.
	other, _ := e.st.CreateUser(store.User{Email: "other@test.dev", PasswordHash: "x", Role: "owner"})
	ot, _ := e.st.CreateAPIToken(store.APIToken{UserID: other.ID, Name: "theirs", TokenHash: "h", Prefix: "dm_x", Scope: "read"})
	e.post(t, "/settings/tokens/"+ot.ID+"/revoke", url.Values{})
	if ts, _ := e.st.ListAPITokens(other.ID); len(ts) != 1 {
		t.Error("a user revoked another user's token")
	}
}
