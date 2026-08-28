package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify normalizes a name into a URL-safe slug: lowercase, runs of
// non-alphanumerics collapsed to a single hyphen.
func slugify(name string) string {
	return strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// viewCtx carries the per-request values every template needs.
type viewCtx = templates.ViewCtx

func (s *Server) viewCtx(r *http.Request) viewCtx {
	vc := viewCtx{Flash: r.URL.Query().Get("flash")}
	if user, ok := auth.UserFromContext(r.Context()); ok {
		vc.Email = user.Email
	}
	if sess, ok := auth.SessionFromContext(r.Context()); ok {
		vc.CSRF = sess.CSRFToken
	}
	return vc
}

func (s *Server) handleProjectsList(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	projects, err := s.store.ListProjects(user.ID)
	if err != nil {
		slog.Error("projects: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, r, http.StatusOK, templates.ProjectsPage(s.viewCtx(r), projects))
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 64 {
		redirectHome(w, r, "Project name must be 1-64 characters.")
		return
	}
	slug := slugify(name)
	if slug == "" {
		redirectHome(w, r, "Project name has no usable characters.")
		return
	}
	project, err := s.store.CreateProject(store.Project{UserID: user.ID, Name: name, Slug: slug})
	if err != nil {
		slog.Error("projects: create", "err", err)
		redirectHome(w, r, "Could not create project (slug may already exist).")
		return
	}
	http.Redirect(w, r, "/projects/"+project.Slug, http.StatusSeeOther)
}

func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	project, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("projects: get", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	apps, err := s.store.ListApps(project.ID)
	if err != nil {
		slog.Error("projects: list apps", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	svcs, err := s.store.ListServices(project.ID)
	if err != nil {
		slog.Error("projects: list services", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, r, http.StatusOK, templates.ProjectPage(s.viewCtx(r), project, apps, svcs))
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	project, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("projects: get for delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.store.DeleteProject(project.ID); err != nil {
		slog.Error("projects: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	redirectHome(w, r, "Project deleted.")
}
