package httpserver

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// audit records a state-changing API call: always in the audit log (accepted
// or refused), and — when it concerns an app — in that app's history, so the
// owner reading the app page sees "Changed through the API: …". Failures to
// write are logged, never fatal to the call.
func (s *Server) audit(r *http.Request, action, target, detail, result, appID string) {
	tok, _ := auth.TokenFromContext(r.Context())
	if err := s.store.RecordAudit(store.AuditEntry{
		TokenID: tok.ID, TokenName: tok.Name, Action: action, Target: target, Detail: clipText(detail, 300), Result: result,
	}); err != nil {
		slog.Error("audit: record", "err", err)
	}
	if appID != "" && result == "ok" {
		msg := fmt.Sprintf("%s %s via API token “%s”", action, target, tok.Name)
		if detail != "" {
			msg += " — " + clipText(detail, 200)
		}
		_ = s.store.RecordEvent(appID, store.EventAPIAction, msg)
	}
}

// actionDone answers an accepted action and refused ones with one shape.
func (s *Server) actionRefused(w http.ResponseWriter, r *http.Request, action, target, refusal, appID string) {
	s.audit(r, action, target, refusal, "refused", appID)
	apiErr(w, http.StatusConflict, refusal)
}

func (s *Server) actionFailed(w http.ResponseWriter, action string, err error) {
	slog.Error("api: "+action, "err", err)
	apiErr(w, http.StatusInternalServerError, "internal error")
}

func (s *Server) queued(w http.ResponseWriter, r *http.Request, action, target, deploymentID, note, appID string) {
	s.audit(r, action, target, "deployment "+deploymentID, "ok", appID)
	out := map[string]any{"ok": true, "deployment_id": deploymentID, "status": "queued"}
	if note != "" {
		out["note"] = note
	}
	apiJSON(w, http.StatusAccepted, out)
}

// POST /api/v1/apps/{slug}/deploy
func (s *Server) handleAPIDeploy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	id, refusal, err := s.deployCore(r.Context(), app)
	switch {
	case err != nil:
		s.actionFailed(w, "deploy", err)
	case refusal != "":
		s.actionRefused(w, r, "deploy", app.Slug, refusal, app.ID)
	default:
		s.queued(w, r, "deploy", app.Slug, id, "", app.ID)
	}
}

// POST /api/v1/apps/{slug}/redeploy
func (s *Server) handleAPIRedeploy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	id, refusal, err := s.redeployCore(app)
	switch {
	case err != nil:
		s.actionFailed(w, "redeploy", err)
	case refusal != "":
		s.actionRefused(w, r, "redeploy", app.Slug, refusal, app.ID)
	default:
		s.queued(w, r, "redeploy", app.Slug, id, "", app.ID)
	}
}

// POST /api/v1/apps/{slug}/run-workflow
func (s *Server) handleAPIRunWorkflow(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	msg, started := s.runWorkflowCore(r.Context(), app)
	if !started {
		s.actionRefused(w, r, "run_workflow", app.Slug, msg, app.ID)
		return
	}
	s.audit(r, "run_workflow", app.Slug, "", "ok", app.ID)
	apiJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": msg})
}

// POST /api/v1/apps/{slug}/{start|stop|restart}
func (s *Server) handleAPILifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		app, ok := s.apiApp(w, r)
		if !ok {
			return
		}
		tok, _ := auth.TokenFromContext(r.Context())
		refusal, err := s.lifecycle(r.Context(), app, action, "the API (token “"+tok.Name+"”)")
		if err != nil {
			s.audit(r, action, app.Slug, err.Error(), "refused", app.ID)
			apiErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if refusal != "" {
			s.actionRefused(w, r, action, app.Slug, refusal, app.ID)
			return
		}
		// lifecycle already wrote the app event; the audit log still gets its line.
		s.audit(r, action, app.Slug, "", "ok", "")
		status := "running"
		if action == "stop" {
			status = "stopped"
		}
		apiJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
	}
}

// POST /api/v1/deployments/{id}/retry
func (s *Server) handleAPIRetry(w http.ResponseWriter, r *http.Request) {
	d, app, ok := s.apiDeployment(w, r)
	if !ok {
		return
	}
	id, note, refusal, err := s.retryCore(r.Context(), d, app)
	switch {
	case err != nil:
		s.actionFailed(w, "retry", err)
	case refusal != "":
		s.actionRefused(w, r, "retry", d.ID, refusal, app.ID)
	default:
		s.queued(w, r, "retry", d.ID, id, note, app.ID)
	}
}

// POST /api/v1/deployments/{id}/rollback
func (s *Server) handleAPIRollback(w http.ResponseWriter, r *http.Request) {
	d, app, ok := s.apiDeployment(w, r)
	if !ok {
		return
	}
	id, refusal, err := s.rollbackCore(d, app)
	switch {
	case err != nil:
		s.actionFailed(w, "rollback", err)
	case refusal != "":
		s.actionRefused(w, r, "rollback", d.ID, refusal, app.ID)
	default:
		s.queued(w, r, "rollback", d.ID, id, "", app.ID)
	}
}
