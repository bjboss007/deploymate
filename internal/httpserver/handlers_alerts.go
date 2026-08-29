package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

func (s *Server) handleAlertsPage(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAlerts()
	if err != nil {
		slog.Error("alerts: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	history := make(map[string][]store.AlertEvent, len(list))
	for _, a := range list {
		events, err := s.store.ListAlertEvents(a.ID, 10)
		if err != nil {
			slog.Error("alerts: list deliveries", "err", err)
			continue
		}
		history[a.ID] = events
	}
	render(w, r, http.StatusOK, templates.AlertsPage(s.viewCtx(r), list, history, alerts.Catalog))
}

func (s *Server) handleAlertCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	url := strings.TrimSpace(r.FormValue("url"))
	if name == "" || len(name) > 64 {
		http.Redirect(w, r, "/alerts?flash="+flashURL("Give the target a name (1-64 characters)."), http.StatusSeeOther)
		return
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		http.Redirect(w, r, "/alerts?flash="+flashURL("Webhook URL must start with http:// or https://"), http.StatusSeeOther)
		return
	}
	var events []string
	for _, e := range alerts.Catalog {
		if r.FormValue("event_"+e.Event) == "on" {
			events = append(events, e.Event)
		}
	}
	if len(events) == 0 {
		http.Redirect(w, r, "/alerts?flash="+flashURL("Pick at least one event."), http.StatusSeeOther)
		return
	}
	urlEnc, err := crypto.Encrypt(s.encKey, url)
	if err != nil {
		slog.Error("alerts: encrypt url", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.CreateAlert(store.Alert{
		Name: name, Channel: "webhook", URLEnc: urlEnc, Enabled: true, Events: events,
	}); err != nil {
		slog.Error("alerts: create", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/alerts?flash="+flashURL("Alert target added."), http.StatusSeeOther)
}

func (s *Server) handleAlertDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAlert(chi.URLParam(r, "id")); err != nil {
		slog.Error("alerts: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/alerts?flash="+flashURL("Alert target removed."), http.StatusSeeOther)
}
