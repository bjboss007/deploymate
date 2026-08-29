package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// handleAppEnvironment sets the app's environment: production or staging.
// The environment selects which manifest overlay (deploymate.{env}.yml)
// and which services apply; the next deploy picks them up.
func (s *Server) handleAppEnvironment(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	env := strings.TrimSpace(r.FormValue("environment"))
	if env != store.EnvStaging && env != store.EnvProduction {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Environment must be staging or production."), http.StatusSeeOther)
		return
	}
	_ = s.store.RecordEvent(app.ID, store.EventEnvironmentChanged, "environment set to "+env)
	if err := s.store.UpdateAppEnvironment(app.ID, env); err != nil {
		slog.Error("apps: set environment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Environment set to "+env+". The next deploy uses its manifest and services."), http.StatusSeeOther)
}
