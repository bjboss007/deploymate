package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// handleAppReleases renders the full per-app deployment list: status,
// commit, duration, trigger, and one-click rollback. ?status=all|successful|
// failed filters the rows.
func (s *Server) handleAppReleases(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		slog.Error("releases: get project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	deployments, err := s.store.ListDeployments(app.ID, 100)
	if err != nil {
		slog.Error("releases: list deployments", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filter := r.URL.Query().Get("status")
	if filter != "successful" && filter != "failed" {
		filter = "all"
	}

	commitURL := func(string) string { return "" }
	if gs, err := s.store.GetGitSource(app.GitSourceID); err == nil {
		commitURL = func(sha string) string { return gitpkg.CommitURL(gs.RepoURL, sha) }
	}

	rows := make([]templates.ReleaseRow, 0, len(deployments))
	for _, d := range deployments {
		if filter == "successful" && d.Status != "running" {
			continue
		}
		if filter == "failed" && d.Status != "failed" {
			continue
		}
		trigger := d.Trigger
		if trigger == "" {
			trigger = d.Kind // legacy rows predate the trigger column
		}
		url := ""
		if d.CommitSHA != "" {
			url = commitURL(d.CommitSHA)
		}
		rows = append(rows, templates.ReleaseRow{
			ID:            d.ID,
			Status:        d.Status,
			Kind:          d.Kind,
			Trigger:       trigger,
			CommitSHA:     d.CommitSHA,
			CommitMessage: d.CommitMessage,
			ImageTag:      d.ImageTag,
			CreatedAt:     d.CreatedAt,
			Duration:      deployDuration(d.StartedAt, d.FinishedAt),
			CommitURL:     url,
			Current:       d.ID == app.CurrentDeploymentID,
			Rollbackable:  d.ImageTag != "" && d.ID != app.CurrentDeploymentID && d.Kind != "rollback" && d.Kind != "scale",
		})
	}

	render(w, r, http.StatusOK, templates.ReleasesPage(s.viewCtx(r), project, app, rows, filter))
}

// deployDuration renders the elapsed build+swap time, or "" when the
// deployment never started or finished.
func deployDuration(startedAt, finishedAt string) string {
	if startedAt == "" || finishedAt == "" {
		return ""
	}
	start, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return ""
	}
	finish, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		return ""
	}
	return finish.Sub(start).Round(time.Second).String()
}
