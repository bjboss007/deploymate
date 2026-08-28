package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.UserFromContext(r.Context()); ok {
		redirectHome(w, r, "")
		return
	}
	render(w, r, http.StatusOK, templates.LoginPage())
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")

	user, err := s.store.GetUserByEmail(email)
	if errors.Is(err, store.ErrNotFound) {
		// Same response as a wrong password: don't leak which emails exist.
		render(w, r, http.StatusUnauthorized, templates.LoginPage("Invalid email or password."))
		return
	}
	if err != nil {
		slog.Error("login: lookup user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		render(w, r, http.StatusUnauthorized, templates.LoginPage("Invalid email or password."))
		return
	}

	sess, token, err := auth.NewSession(user.ID)
	if err != nil {
		slog.Error("login: create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.CreateSession(sess); err != nil {
		slog.Error("login: store session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
		Expires:  time.Now().Add(auth.SessionTTL),
	})
	redirectHome(w, r, "")
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if sess, ok := auth.SessionFromContext(r.Context()); ok {
		_ = s.store.DeleteSession(sess.ID)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
