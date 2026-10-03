package httpserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/habibmuhammad/deploymate/internal/backup"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// serviceBackupView assembles the service page's backup panel: the stored
// config (defaults when no row yet) plus the current object listing. The
// listing is a live destination read — the bucket is the catalog.
func (s *Server) serviceBackupView(r *http.Request, svc store.Service) templates.BackupView {
	bk := templates.BackupView{
		Schedule:    store.DefaultBackupSchedule,
		Keep:        store.DefaultBackupKeep,
		Destination: "default",
	}
	if s.backups == nil {
		return bk
	}
	bk.HasDests = len(s.backups.DestinationIDs()) > 0
	bk.DestIDs = s.backups.DestinationIDs()
	if cfg, err := s.store.GetBackupConfig(svc.ID); err == nil {
		bk.Enabled = cfg.Enabled
		bk.Schedule = cfg.Schedule
		bk.Keep = cfg.Keep
		bk.Destination = cfg.Destination
	}
	objs, err := s.backups.ListBackups(r.Context(), svc)
	if err != nil {
		slog.Warn("backups: list", "slug", svc.Slug, "err", err)
		return bk
	}
	for _, o := range objs {
		bk.Objects = append(bk.Objects, templates.BackupObject{
			Key:     o.Key,
			TakenAt: o.TakenAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
			Size:    o.Size,
			SHA:     o.SHA,
		})
	}
	return bk
}

// handleBackupConfigSave is the opt-in/settings form: POST /services/{slug}/backup.
func (s *Server) handleBackupConfigSave(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if s.backups == nil {
		http.Error(w, "backups not configured", http.StatusInternalServerError)
		return
	}
	keep, err := strconv.Atoi(r.FormValue("keep"))
	if err != nil {
		keep = 0
	}
	cfg := store.BackupConfig{
		Enabled:     r.FormValue("enabled") != "",
		Schedule:    r.FormValue("schedule"),
		Keep:        keep,
		Destination: r.FormValue("destination"),
	}
	if err := s.backups.SaveConfig(svc, cfg); err != nil {
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Save failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	if cfg.Enabled {
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Backups enabled. Download the backup key below and keep a copy off-box."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Backups disabled — existing backups stay listable and restorable."), http.StatusSeeOther)
}

// handleBackupNow is the manual run: POST /services/{slug}/backup/now.
func (s *Server) handleBackupNow(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if s.backups == nil {
		http.Error(w, "backups not configured", http.StatusInternalServerError)
		return
	}
	switch err := s.backups.BackupNow(svc); {
	case errors.Is(err, backup.ErrDisabled), errors.Is(err, backup.ErrNoKey), errors.Is(err, backup.ErrNotPostgres):
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL(err.Error()), http.StatusSeeOther)
	case errors.Is(err, backup.ErrInFlight):
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("A backup or restore is already running for this service."), http.StatusSeeOther)
	case err != nil:
		slog.Error("backup now", "slug", svc.Slug, "err", err)
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Backup could not be started: "+err.Error()), http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Backup started — watch the events timeline."), http.StatusSeeOther)
	}
}

// handleBackupKey downloads the service's backup key as plain hex. Same
// trust gate as webhook-secret viewing: the logged-in owner's session.
func (s *Server) handleBackupKey(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if s.backups == nil {
		notFoundPage(w, r)
		return
	}
	hexKey, err := s.backups.ServiceKeyHex(svc.ID)
	if errors.Is(err, backup.ErrDisabled) || errors.Is(err, backup.ErrNoKey) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		slog.Error("backup key", "slug", svc.Slug, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.backup.key"`, svc.Slug))
	_, _ = w.Write([]byte(hexKey + "\n"))
}

// handleBackupRestore restores one stored object into the service's
// database. Destructive and human-gated: the form's confirm field must
// equal the service slug (the client prompt is UX — this check is the
// gate).
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.serviceFromRequest(w, r)
	if !ok {
		return
	}
	if r.FormValue("confirm") != svc.Slug {
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Restore cancelled — type the service name to confirm."), http.StatusSeeOther)
		return
	}
	if s.backups == nil {
		http.Error(w, "backups not configured", http.StatusInternalServerError)
		return
	}
	objectKey := r.FormValue("object_key")
	switch err := s.backups.Restore(svc, objectKey); {
	case errors.Is(err, backup.ErrBadObject):
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("That object is not one of this service's backups."), http.StatusSeeOther)
	case errors.Is(err, backup.ErrNoKey):
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("No backup key recorded for this service — it can restore nothing."), http.StatusSeeOther)
	case errors.Is(err, backup.ErrInFlight):
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("A backup or restore is already running for this service."), http.StatusSeeOther)
	case err != nil:
		slog.Error("restore", "slug", svc.Slug, "err", err)
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Restore could not be started: "+err.Error()), http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/services/"+svc.Slug+"?flash="+flashURL("Restore started — the database is dropped and recreated; watch the events timeline."), http.StatusSeeOther)
	}
}
