package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// SessionCookieName is the browser cookie holding the session token.
const SessionCookieName = "dm_session"

type ctxKey int

const (
	userKey ctxKey = iota
	sessionKey
)

// Middleware provides session loading, authentication, and CSRF checks.
type Middleware struct {
	Store *store.Store
}

// UserFromContext returns the authenticated user, if any.
func UserFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userKey).(store.User)
	return u, ok
}

// SessionFromContext returns the current session record, if any.
func SessionFromContext(ctx context.Context) (store.Session, bool) {
	s, ok := ctx.Value(sessionKey).(store.Session)
	return s, ok
}

// LoadSession resolves the session cookie into user + session context values.
// Missing or expired sessions pass through unauthenticated.
func (m *Middleware) LoadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		sess, err := m.Store.GetSessionByTokenHash(HashToken(cookie.Value))
		if errors.Is(err, store.ErrNotFound) {
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		expires, err := time.Parse(time.RFC3339Nano, sess.ExpiresAt)
		if err != nil || time.Now().After(expires) {
			_ = m.Store.DeleteSession(sess.ID)
			next.ServeHTTP(w, r)
			return
		}
		user, err := m.Store.GetUserByID(sess.UserID)
		if errors.Is(err, store.ErrNotFound) {
			next.ServeHTTP(w, r)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, sess)
		ctx = context.WithValue(ctx, userKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireUser redirects unauthenticated requests to /login.
func (m *Middleware) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFromContext(r.Context()); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CheckCSRF verifies the form's csrf_token field against the session token.
// It must run after LoadSession + RequireUser.
func (m *Middleware) CheckCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := SessionFromContext(r.Context())
		if !ok {
			http.Error(w, "no session", http.StatusForbidden)
			return
		}
		got := r.FormValue("csrf_token")
		want := sess.CSRFToken
		if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
