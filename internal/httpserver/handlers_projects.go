package httpserver

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/services"
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
	vc := viewCtx{Flash: r.URL.Query().Get("flash"), Path: r.URL.Path}
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
	now := time.Now()
	groups := make([]templates.ProjectGroup, 0, len(projects))
	for _, p := range projects {
		apps, err := s.store.ListApps(p.ID)
		if err != nil {
			slog.Error("projects: list apps", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		svcs, err := s.store.ListServices(p.ID)
		if err != nil {
			slog.Error("projects: list services", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		rows := s.appRows(apps, now)
		applyResourceHealth(rows, svcs, s.exclusionsFor(apps))
		for i := range rows {
			rows[i].Project = p.Name
		}
		groups = append(groups, templates.ProjectGroup{Project: p, Rows: rows, Services: svcs})
	}
	render(w, r, http.StatusOK, templates.ProjectsPage(s.viewCtx(r), groups, summarize(groups)))
}

func (s *Server) handleProjectCreate(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	project, refusal, err := s.createProjectCore(user.ID, r.FormValue("name"))
	if err != nil || refusal != "" {
		redirectHome(w, r, refusal)
		return
	}
	http.Redirect(w, r, "/projects/"+project.Slug, http.StatusSeeOther)
}

func (s *Server) handleProjectDetail(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	project, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
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
	rows := s.appRows(apps, time.Now())
	excl := s.exclusionsFor(apps)
	applyResourceHealth(rows, svcs, excl)
	cards, unused := buildAppCards(rows, svcs, excl, s.serviceUsage(r.Context(), apps, svcs, excl))
	render(w, r, http.StatusOK, templates.ProjectPage(s.viewCtx(r), project, cards, unused, len(svcs) > 0, s.placement(project), envMismatchHint(apps, unused)))
}

func (s *Server) handleProjectDelete(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	project, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		slog.Error("projects: get for delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The containers go first: once the rows are gone nothing would find them again, and on a node they would run on
	// someone else's machine for good. A container that cannot be removed (an unreachable node) is named in the message.
	var stuck []string
	if apps, err := s.store.ListApps(project.ID); err == nil {
		for _, app := range apps {
			stuck = append(stuck, s.removeAppContainers(r.Context(), app)...)
		}
	}
	if svcs, err := s.store.ListServices(project.ID); err == nil {
		for _, svc := range svcs {
			if !s.removeServiceContainer(r.Context(), svc) {
				stuck = append(stuck, services.ContainerName(svc.Slug))
			}
		}
	}
	if err := s.store.DeleteProject(project.ID); err != nil {
		slog.Error("projects: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if len(stuck) > 0 {
		redirectHome(w, r, "Project deleted, but these containers could not be removed and may still be running: "+strings.Join(stuck, ", ")+". Remove them by hand.")
		return
	}
	redirectHome(w, r, "Project deleted.")
}

// envMismatchHint explains why a database is not used: apps only get the databases of their own environment, so a database in
// another environment than every app is the usual reason. "" when that is not the situation.
func envMismatchHint(apps []store.App, unused []templates.ResourceChip) string {
	if len(apps) == 0 || len(unused) == 0 {
		return ""
	}
	appEnvs := map[string]bool{}
	var appList []string
	for _, a := range apps {
		if !appEnvs[a.Environment] {
			appList = append(appList, a.Environment)
		}
		appEnvs[a.Environment] = true
	}
	var off []string
	for _, c := range unused {
		if !appEnvs[c.Service.Environment] {
			off = append(off, c.Service.Name+" ("+c.Service.Environment+")")
		}
	}
	if len(off) == 0 {
		return ""
	}
	return "Apps get the databases of their own environment. Your apps run in " + strings.Join(appList, " and ") + "; " +
		strings.Join(off, ", ") + " is in another one. Change the app's environment in its settings, or create the database in the app's environment."
}
