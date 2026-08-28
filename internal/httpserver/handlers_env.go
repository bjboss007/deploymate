package httpserver

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s *Server) handleEnvVarCreate(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	value := r.FormValue("value")
	isSecret := r.FormValue("is_secret") == "on"
	if !envKeyRe.MatchString(key) || len(key) > 128 {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Keys must be letters, digits, and underscores, starting with a letter."), http.StatusSeeOther)
		return
	}
	valueEnc, err := crypto.Encrypt(s.encKey, value)
	if err != nil {
		slog.Error("env: encrypt", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.UpsertEnvVar(store.EnvVar{
		AppID: app.ID, Key: key, ValueEnc: valueEnc, IsSecret: isSecret,
	}); err != nil {
		slog.Error("env: upsert", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Environment variable saved. Redeploy to apply."), http.StatusSeeOther)
}

func (s *Server) handleEnvVarDelete(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteEnvVar(chi.URLParam(r, "id")); err != nil {
		slog.Error("env: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Environment variable removed. Redeploy to apply."), http.StatusSeeOther)
}
