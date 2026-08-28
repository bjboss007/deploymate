package httpserver

import (
	"errors"
	"fmt"
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

func dmServiceName(slug string) string { return "dm-svc-" + slug }

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
	render(w, r, http.StatusOK, templates.ServicePage(s.viewCtx(r), project, svc, tpl.Label))
}

// handleServiceStart provisions (first run) or resumes a service: creds,
// volume, container, and a readiness wait.
func (s *Server) handleServiceStart(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	tpl, _ := services.ForType(svc.Type)
	ctx := r.Context()
	name := dmServiceName(svc.Slug)
	fail := func(err error) {
		slog.Error("services: start", "service", svc.Slug, "err", err)
		_ = s.store.UpdateServiceStatus(svc.ID, "failed")
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Start failed: "+err.Error()), http.StatusSeeOther)
	}

	// Credentials: keep existing, generate on first run.
	credsEnc, err := s.store.GetServiceCredentials(svc.ID)
	if err != nil {
		fail(err)
		return
	}
	if len(credsEnc) == 0 {
		plain := make(map[string]string, len(tpl.CredsGen))
		enc := make(map[string]string, len(tpl.CredsGen))
		for k, gen := range tpl.CredsGen {
			plain[k] = gen()
			enc[k], err = crypto.Encrypt(s.encKey, plain[k])
			if err != nil {
				fail(err)
				return
			}
		}
		if err := s.store.SetServiceCredentials(svc.ID, enc); err != nil {
			fail(err)
			return
		}
		credsEnc = enc
	}
	creds := s.decryptCreds(credsEnc)

	has, err := s.rt.HasImage(ctx, tpl.Image)
	if err != nil {
		fail(err)
		return
	}
	if !has {
		if err := s.rt.PullImage(ctx, tpl.Image); err != nil {
			fail(err)
			return
		}
	}
	if err := s.rt.EnsureNetwork(ctx, NetworkName); err != nil {
		fail(err)
		return
	}
	_ = s.rt.Stop(ctx, name, 5)
	if err := s.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		fail(err)
		return
	}
	spec := runtime.Spec{
		Name:    name,
		Image:   tpl.Image,
		Env:     tpl.Env(creds),
		Labels:  dmServiceLabels(svc.Slug, svc.Type),
		Network: NetworkName,
		Binds:   []string{svc.VolumeName + ":" + tpl.Mount},
	}
	if _, err := s.rt.Create(ctx, spec); err != nil {
		fail(err)
		return
	}
	if err := s.rt.Start(ctx, name); err != nil {
		fail(err)
		return
	}

	// Wait for readiness so "running" means "accepting connections".
	ready := false
	for i := 0; i < 30; i++ {
		if _, err := s.rt.Exec(ctx, name, tpl.ReadyCmd(creds)); err == nil {
			ready = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !ready {
		fail(fmt.Errorf("service did not become ready within 60s — check its logs"))
		return
	}

	if err := s.store.UpdateServiceStatus(svc.ID, "running"); err != nil {
		slog.Error("services: set running", "err", err)
	}
	http.Redirect(w, r, "/services/"+svc.Slug, http.StatusSeeOther)
}

func (s *Server) handleServiceStop(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	name := dmServiceName(svc.Slug)
	if err := s.rt.Stop(r.Context(), name, 10); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		slog.Error("services: stop", "err", err)
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Stop failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.UpdateServiceStatus(svc.ID, "stopped")
	http.Redirect(w, r, "/services/"+svc.Slug, http.StatusSeeOther)
}

func (s *Server) handleServiceDelete(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	name := dmServiceName(svc.Slug)
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
		http.NotFound(w, r)
		return svc, false
	}
	if err != nil {
		slog.Error("service lookup", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return svc, false
	}
	return svc, true
}

func dmServiceLabels(slug, svcType string) map[string]string {
	return map[string]string{
		"deploymate.managed": "true",
		"deploymate.service": slug,
		"deploymate.type":    svcType,
	}
}
