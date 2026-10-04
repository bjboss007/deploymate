package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// The JSON API (/api/v1) is what the MCP server and scripts talk to. It is a
// thin, honest layer over the same store and worker queue as the dashboard,
// and it NEVER returns a secret: environment values, connection strings,
// tokens and webhook secrets are not in any response (docs/decisions/0020).

const maxAPIBody = 1 << 20 // 1 MiB

func apiJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, msg string) { auth.APIError(w, status, msg) }

// apiBody decodes the JSON request body into v. An empty body is fine (v keeps
// its zero value) so action endpoints with no arguments take no body at all.
func apiBody(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAPIBody))
	if err != nil {
		apiErr(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	if len(b) == 0 {
		return true
	}
	if err := json.Unmarshal(b, v); err != nil {
		apiErr(w, http.StatusBadRequest, "the body is not valid JSON of the expected shape")
		return false
	}
	return true
}

// apiInt reads a bounded integer query parameter.
func apiInt(r *http.Request, name string, def, min, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return def
	}
	return min + clampInt(n-min, 0, max-min)
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func (s *Server) apiApp(w http.ResponseWriter, r *http.Request) (store.App, bool) {
	app, err := s.store.GetAppBySlug(chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		apiErr(w, http.StatusNotFound, "no such app")
		return app, false
	}
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return app, false
	}
	return app, true
}

func (s *Server) apiProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	user, _ := auth.UserFromContext(r.Context())
	p, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		apiErr(w, http.StatusNotFound, "no such project")
		return p, false
	}
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return p, false
	}
	return p, true
}

func (s *Server) apiService(w http.ResponseWriter, r *http.Request) (store.Service, bool) {
	sv, err := s.store.GetServiceBySlug(chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		apiErr(w, http.StatusNotFound, "no such service")
		return sv, false
	}
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return sv, false
	}
	return sv, true
}

func (s *Server) apiDeployment(w http.ResponseWriter, r *http.Request) (store.Deployment, store.App, bool) {
	d, err := s.store.GetDeployment(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		apiErr(w, http.StatusNotFound, "no such deployment")
		return d, store.App{}, false
	}
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return d, store.App{}, false
	}
	app, err := s.store.GetAppByID(d.AppID)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return d, app, false
	}
	return d, app, true
}
