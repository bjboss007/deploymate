package httpserver

import (
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// hostnamePattern accepts standard DNS hostnames (labels ≤63 chars).
func hostnamePattern() *regexp.Regexp {
	return regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
}

var hostnameRe = hostnamePattern()

func (s *Server) handleDomainCreate(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if _, refusal, _ := s.addDomainCore(app, r.FormValue("hostname")); refusal != "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Domain added. Redeploy to route it (and issue its certificate)."), http.StatusSeeOther)
}

func (s *Server) handleDomainDelete(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteDomain(chi.URLParam(r, "id")); err != nil {
		slog.Error("domains: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Domain removed. Redeploy to update routing."), http.StatusSeeOther)
}

// handleAppPort sets the app's routing port (Traefik + health checks).
func (s *Server) handleAppPort(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	port := 0
	if p := strings.TrimSpace(r.FormValue("port")); p != "" {
		if v, err := strconv.Atoi(p); err == nil && v > 0 && v < 65536 {
			port = v
		}
	}
	if port == 0 {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Port must be a number between 1 and 65535."), http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateAppPort(app.ID, port); err != nil {
		slog.Error("apps: set port", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Routing port saved. Redeploy to apply."), http.StatusSeeOther)
}
