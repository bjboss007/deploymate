package httpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// maxAPITokens caps tokens per user — a runaway script cannot fill the table.
const maxAPITokens = 20

func (s *Server) handleTokensPage(w http.ResponseWriter, r *http.Request) {
	s.renderTokens(w, r, "")
}

// renderTokens draws the page; created is the plaintext of a token just made
// (shown this once, never stored, never in a URL).
func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, created string) {
	user, _ := auth.UserFromContext(r.Context())
	list, err := s.store.ListAPITokens(user.ID)
	if err != nil {
		slog.Error("tokens: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	audit, err := s.store.ListAudit(25)
	if err != nil {
		slog.Error("tokens: list audit", "err", err)
	}
	w.Header().Set("Cache-Control", "no-store") // a fresh token must not sit in a cache
	render(w, r, http.StatusOK, templates.TokensPage(s.viewCtx(r), list, audit, created, time.Now()))
}

func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	back := func(msg string) {
		http.Redirect(w, r, "/settings/tokens?flash="+flashURL(msg), http.StatusSeeOther)
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 64 {
		back("Give the token a name (1-64 characters), such as the tool that will use it.")
		return
	}
	scope := r.FormValue("scope")
	if !store.ValidScope(scope) {
		back("Pick what the token may do.")
		return
	}
	expires := ""
	switch r.FormValue("expires") {
	case "30":
		expires = time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339Nano)
	case "90":
		expires = time.Now().UTC().Add(90 * 24 * time.Hour).Format(time.RFC3339Nano)
	case "365":
		expires = time.Now().UTC().Add(365 * 24 * time.Hour).Format(time.RFC3339Nano)
	case "never":
	default:
		back("Pick when the token expires.")
		return
	}
	if n, err := s.store.CountAPITokens(user.ID); err == nil && n >= maxAPITokens {
		back("You already have 20 tokens — revoke one you no longer use first.")
		return
	}
	plain, hash, prefix, err := auth.NewAPIToken()
	if err != nil {
		slog.Error("tokens: generate", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.CreateAPIToken(store.APIToken{
		UserID: user.ID, Name: name, TokenHash: hash, Prefix: prefix, Scope: scope, ExpiresAt: expires,
	}); err != nil {
		slog.Error("tokens: create", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderTokens(w, r, plain)
}

func (s *Server) handleTokenRevoke(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	ok, err := s.store.DeleteAPIToken(chi.URLParam(r, "id"), user.ID)
	if err != nil {
		slog.Error("tokens: revoke", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	msg := "Token revoked. Anything using it stops working immediately."
	if !ok {
		msg = "That token no longer exists."
	}
	http.Redirect(w, r, "/settings/tokens?flash="+flashURL(msg), http.StatusSeeOther)
}

// handleAPIWhoami is the first /api/v1 endpoint: it answers who the token acts
// as and what it may do — enough for a client to check its configuration.
func (s *Server) handleAPIWhoami(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	tok, _ := auth.TokenFromContext(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"user":       user.Email,
		"token":      tok.Name,
		"scope":      tok.Scope,
		"expires_at": tok.ExpiresAt,
	})
}
