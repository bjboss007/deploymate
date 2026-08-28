package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/habibmuhammad/deploymate/internal/builder"
)

// handleAppRuntime sets the app's build method: Dockerfile (empty) or a
// runtime from builder.Runtimes, optionally version-pinned ("node:22").
func (s *Server) handleAppRuntime(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	raw := r.FormValue("runtime")
	if raw == "dockerfile" {
		raw = ""
	}
	if v := r.FormValue("version"); v != "" && raw != "" && !containsColon(raw) {
		raw = raw + ":" + v
	}
	normalized, err := builder.ValidateRuntimeSpec(raw)
	if err != nil {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(err.Error()), http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateAppRuntime(app.ID, normalized); err != nil {
		slog.Error("apps: set runtime", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if normalized == "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Build method set to Dockerfile. Deploy to rebuild."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Runtime saved — the next deploy builds with "+normalized+" and no Dockerfile."), http.StatusSeeOther)
}

func containsColon(s string) bool {
	for _, c := range s {
		if c == ':' {
			return true
		}
	}
	return false
}
