package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// decryptCreds decrypts an encrypted credential map.
func (s *Server) decryptCreds(enc map[string]string) map[string]string {
	out := make(map[string]string, len(enc))
	for k, v := range enc {
		plain, err := crypto.Decrypt(s.encKey, v)
		if err != nil {
			slog.Error("services: decrypt cred", "key", k, "err", err)
			continue
		}
		out[k] = plain
	}
	return out
}

func (s *Server) handleServiceCreate(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromRequest(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	svcType := r.FormValue("type")
	tpl, known := services.ForType(svcType)
	if name == "" || len(name) > 64 {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("Service name must be 1-64 characters."), http.StatusSeeOther)
		return
	}
	if !known {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("Unknown service type."), http.StatusSeeOther)
		return
	}
	slug := slugify(name)
	if slug == "" {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("Service name has no usable characters."), http.StatusSeeOther)
		return
	}
	svc, err := s.store.CreateService(store.Service{
		ProjectID: project.ID, Type: svcType, Name: name, Slug: slug,
		Image: tpl.Image, Status: "stopped", VolumeName: services.VolumeName(slug), Port: tpl.Port,
	})
	if errors.Is(err, store.ErrSlugTaken) {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("That name is already taken."), http.StatusSeeOther)
		return
	}
	if err != nil {
		slog.Error("services: create", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/services/"+svc.Slug, http.StatusSeeOther)
}

func (s *Server) handleServicePage(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetProjectByID(svc.ProjectID)
	if err != nil {
		slog.Error("services: get project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tpl, _ := services.ForType(svc.Type)

	// The owner may see their own connection string — copying it into an
	// env var is the escape hatch for apps with custom expectations.
	connURL := ""
	if svc.Status == "running" {
		if credsEnc, err := s.store.GetServiceCredentials(svc.ID); err == nil && len(credsEnc) > 0 {
			creds := s.decryptCreds(credsEnc)
			connURL = tpl.ConnURL(creds, services.ContainerName(svc.Slug))
		}
	}
	// Apps that receive this service's connection URL: the same project and
	// environment (the injection rule).
	var users []templates.ServiceUser
	if apps, err := s.store.ListApps(project.ID); err == nil {
		var envApps []store.App
		for _, a := range apps {
			if a.Environment == svc.Environment {
				envApps = append(envApps, a)
			}
		}
		excl := s.exclusionsFor(envApps)
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		usage := s.serviceUsage(ctx, envApps, []store.Service{svc}, excl)
		for _, a := range envApps {
			users = append(users, templates.ServiceUser{App: a, Excluded: excl[a.ID][svc.ID], Usage: usage[a.ID][svc.ID]})
		}
	}
	render(w, r, http.StatusOK, templates.ServicePage(s.viewCtx(r), project, svc, tpl.Label, tpl.URLEnv, connURL, s.serviceBackupView(r, svc), users))
}

// handleServiceStart provisions (first run) or resumes a service via the
// shared provisioner — the same code path the deploy worker uses for
// manifest-declared services.
func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.prov.Provision(r.Context(), svc); err != nil {
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Start failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/services/"+svc.Slug, http.StatusSeeOther)
}

// handleServiceRestart stops and re-provisions a service in one click.
func (s *Server) handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.prov.Restart(r.Context(), svc); err != nil {
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Restart failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.RecordEvent("", store.EventServiceRestarted, "service "+svc.Name+" restarted and ready")
	http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Service restarted and ready."), http.StatusSeeOther)
}

func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	name := services.ContainerName(svc.Slug)
	if err := s.rt.Stop(r.Context(), name, 10); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		slog.Error("services: stop", "err", err)
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Stop failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.UpdateServiceStatus(svc.ID, "stopped")
	http.Redirect(w, r, "/services/"+svc.Slug, http.StatusSeeOther)
}

// handleServiceKeep is the "Keep" side of orphan surfacing: a human claims an
// orphaned, manifest-created service as their own. Origin flips to manual so
// deploys stop flagging it, and the orphaned badge clears.
func (s *Server) handleServiceKeep(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.store.AdoptService(svc.ID); err != nil {
		slog.Error("services: adopt", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Kept. This service is now managed manually and won't be flagged by deploys."), http.StatusSeeOther)
}

func (s *Server) handleServiceDelete(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	name := services.ContainerName(svc.Slug)
	_ = s.rt.Stop(r.Context(), name, 5)
	if err := s.rt.Remove(r.Context(), name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		slog.Error("services: remove container", "err", err)
	}
	if err := s.store.DeleteService(svc.ID); err != nil {
		slog.Error("services: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	project, _ := s.store.GetProjectByID(svc.ProjectID)
	http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("Service deleted. Its data volume was kept."), http.StatusSeeOther)
}

func (s *Server) serviceFromRequest(w http.ResponseWriter, r *http.Request) (store.Service, bool) {
	svc, err := s.store.GetServiceBySlug(chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return svc, false
	}
	if err != nil {
		slog.Error("service lookup", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return svc, false
	}
	return svc, true
}
