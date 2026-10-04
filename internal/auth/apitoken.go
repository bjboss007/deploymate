package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// TokenPrefix marks a DeployMate API token, so a leaked one is recognisable
// (and secret scanners can match it).
const TokenPrefix = "dm_"

// NewAPIToken makes a token: the plaintext (shown once), the hash to store,
// and the short prefix to recognise it by in lists.
func NewAPIToken() (plaintext, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	plaintext = TokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	return plaintext, HashToken(plaintext), plaintext[:len(TokenPrefix)+6], nil
}

const tokenKey ctxKey = iota + 10

// TokenFromContext returns the API token that authenticated the request.
func TokenFromContext(ctx context.Context) (store.APIToken, bool) {
	t, ok := ctx.Value(tokenKey).(store.APIToken)
	return t, ok
}

// RequireAPIToken authenticates /api requests by "Authorization: Bearer dm_…".
// It deliberately ignores the session cookie: a browser the owner is logged
// in to must never be able to drive the API through a cross-site request.
// Failures answer with JSON and say nothing about WHY a token failed beyond
// "invalid or expired" — no oracle for guessing.
//
// A read token may only GET/HEAD; anything else needs a write token.
func (m *Middleware) RequireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		plain, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || !strings.HasPrefix(plain, TokenPrefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="deploymate"`)
			APIError(w, http.StatusUnauthorized, "send an API token as 'Authorization: Bearer dm_…'")
			return
		}
		tok, err := m.Store.GetAPITokenByHash(HashToken(plain))
		if errors.Is(err, store.ErrNotFound) || (err == nil && tok.Expired(time.Now())) {
			APIError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if err != nil {
			APIError(w, http.StatusInternalServerError, "internal error")
			return
		}
		user, err := m.Store.GetUserByID(tok.UserID)
		if err != nil {
			APIError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		if tok.Scope != store.ScopeWrite && r.Method != http.MethodGet && r.Method != http.MethodHead {
			APIError(w, http.StatusForbidden, "this token is read-only; create a read & act token to change things")
			return
		}
		_ = m.Store.TouchAPIToken(tok.ID)
		ctx := context.WithValue(r.Context(), tokenKey, tok)
		ctx = context.WithValue(ctx, userKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// APIError writes the JSON error body every /api failure uses.
func APIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
