package httpserver

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// isHTTPS reports whether the visitor's connection is HTTPS. DeployMate often sits
// behind something that ends TLS for it (a Cloudflare Tunnel, Traefik), so it also
// believes X-Forwarded-Proto — but ONLY from a loopback peer: the dashboard listens
// on 127.0.0.1, so a header from anywhere else is a client making things up.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

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

	ip := s.clientAddr(r)
	if blocked, wait := s.loginLimit.blocked(ip, email); blocked {
		mins := int(wait.Minutes()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		render(w, r, http.StatusTooManyRequests, templates.LoginPage("Too many failed sign-ins. Try again in "+strconv.Itoa(mins)+" minutes."))
		return
	}

	user, err := s.store.GetUserByEmail(email)
	if errors.Is(err, store.ErrNotFound) {
		// Same response as a wrong password: don't leak which emails exist.
		s.loginFailed(ip, email)
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
		s.loginFailed(ip, email)
		render(w, r, http.StatusUnauthorized, templates.LoginPage("Invalid email or password."))
		return
	}
	s.loginLimit.succeed(ip)
	s.auditLogin("login", email, ip, "ok")

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
		Secure:   isHTTPS(r),
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

// auditLogin records a dashboard sign-in in the audit log (the token fields hold "dashboard").
func (s *Server) auditLogin(action, email, ip, result string) {
	if len(email) > 100 {
		email = email[:100]
	}
	_ = s.store.RecordAudit(store.AuditEntry{TokenName: "dashboard", Action: action, Target: email, Detail: ip, Result: result})
}

func (s *Server) loginFailed(ip, email string) {
	s.auditLogin("login", email, ip, "refused")
	if s.loginLimit.fail(ip, email) && s.notify != nil {
		go s.notify(alerts.EventLoginBlocked, "sign-ins blocked for "+ip,
			"Too many failed dashboard sign-ins (last tried: "+email+"). Further attempts are refused for up to 15 minutes.")
	}
}

// handleLogoutAll ends every session of the signed-in user, this one included.
func (s *Server) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	if u, ok := auth.UserFromContext(r.Context()); ok {
		_ = s.store.DeleteSessionsForUser(u.ID)
	}
	s.handleLogout(w, r)
}
