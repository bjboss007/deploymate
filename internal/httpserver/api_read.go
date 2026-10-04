package httpserver

import (
	"net/http"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/builder"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// ---- response shapes (never a secret in any of them) ----------------------

type apiDeploymentJSON struct {
	ID          string `json:"id"`
	App         string `json:"app,omitempty"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Trigger     string `json:"trigger,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
	CIRunNumber int    `json:"ci_run_number,omitempty"`
	CreatedAt   string `json:"created_at"`
	FinishedAt  string `json:"finished_at,omitempty"`
}

func toAPIDeployment(d store.Deployment, appSlug string) apiDeploymentJSON {
	c := d.CommitSHA
	if len(c) > 12 {
		c = c[:12]
	}
	return apiDeploymentJSON{
		ID: d.ID, App: appSlug, Kind: d.Kind, Status: d.Status, Trigger: d.Trigger, Commit: c,
		Message: clipText(d.CommitMessage, 200), Error: clipText(d.Error, 600),
		CIRunNumber: d.CIRunNumber, CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt,
	}
}

type apiAppJSON struct {
	Slug        string             `json:"slug"`
	Name        string             `json:"name"`
	Project     string             `json:"project,omitempty"`
	Environment string             `json:"environment"`
	Status      string             `json:"status"`
	Health      string             `json:"health,omitempty"`
	Attention   string             `json:"attention,omitempty"` // "" fine | warn | bad
	Reason      string             `json:"reason,omitempty"`
	DeployMode  string             `json:"deploy_mode"`
	Framework   string             `json:"framework,omitempty"`
	URL         string             `json:"url,omitempty"`
	LastDeploy  *apiDeploymentJSON `json:"last_deploy,omitempty"`
	UptimePct   *float64           `json:"uptime_24h_pct,omitempty"`
}

func (s *Server) toAPIApp(r *http.Request, row templates.AppRow, project string) apiAppJSON {
	a := row.App
	out := apiAppJSON{
		Slug: a.Slug, Name: a.Name, Project: project, Environment: a.Environment,
		Status: a.Status, Health: a.Health, Attention: row.Attention, Reason: row.Reason,
		DeployMode: a.DeployMode, Framework: row.Identity.Label, URL: s.previewURL(r, a),
	}
	if out.DeployMode == "" {
		out.DeployMode = store.DeployModeBuild
	}
	if row.Last != nil {
		d := toAPIDeployment(*row.Last, a.Slug)
		out.LastDeploy = &d
	}
	if row.UptimeKnown {
		p := row.UptimePct
		out.UptimePct = &p
	}
	return out
}

type apiServiceJSON struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Project     string   `json:"project,omitempty"`
	Type        string   `json:"type"`
	Environment string   `json:"environment"`
	Status      string   `json:"status"`
	Image       string   `json:"image"`
	Origin      string   `json:"origin,omitempty"`
	Orphaned    bool     `json:"orphaned,omitempty"`
	UsedBy      []string `json:"used_by,omitempty"` // app slugs that receive its connection URL
}

func toAPIService(sv store.Service, project string, usedBy []string) apiServiceJSON {
	return apiServiceJSON{
		Slug: sv.Slug, Name: sv.Name, Project: project, Type: sv.Type, Environment: sv.Environment,
		Status: sv.Status, Image: sv.Image, Origin: sv.Origin, Orphaned: sv.Orphaned, UsedBy: usedBy,
	}
}

// consumers lists the apps (of the service's project and environment) that get
// its connection URL — the same rule AppEnv uses.
func (s *Server) consumers(sv store.Service) []string {
	apps, err := s.store.ListApps(sv.ProjectID)
	if err != nil {
		return nil
	}
	var out []string
	for _, a := range apps {
		if a.Environment != sv.Environment {
			continue
		}
		if ex, _ := s.store.ListAppServiceExclusions(a.ID); ex[sv.ID] {
			continue
		}
		out = append(out, a.Slug)
	}
	return out
}

// ---- handlers --------------------------------------------------------------

// GET /api/v1/fleet — one call for "how is everything?": counts, what needs
// attention first, then every project with its apps and services.
func (s *Server) handleAPIFleet(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	projects, err := s.store.ListProjects(user.ID)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := time.Now()
	groups := make([]templates.ProjectGroup, 0, len(projects))
	type projectJSON struct {
		Slug     string           `json:"slug"`
		Name     string           `json:"name"`
		Apps     []apiAppJSON     `json:"apps"`
		Services []apiServiceJSON `json:"services"`
	}
	out := []projectJSON{}
	attention := []apiAppJSON{}
	for _, p := range projects {
		apps, err := s.store.ListApps(p.ID)
		if err != nil {
			apiErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		svcs, err := s.store.ListServices(p.ID)
		if err != nil {
			apiErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		rows := s.appRows(apps, now)
		applyResourceHealth(rows, svcs, s.exclusionsFor(apps))
		groups = append(groups, templates.ProjectGroup{Project: p, Rows: rows, Services: svcs})
		pj := projectJSON{Slug: p.Slug, Name: p.Name, Apps: []apiAppJSON{}, Services: []apiServiceJSON{}}
		for _, row := range rows {
			a := s.toAPIApp(r, row, p.Name)
			pj.Apps = append(pj.Apps, a)
			if row.Attention == "bad" {
				attention = append(attention, a)
			}
		}
		for _, sv := range svcs {
			pj.Services = append(pj.Services, toAPIService(sv, p.Name, nil))
		}
		out = append(out, pj)
	}
	sm := summarize(groups)
	apiJSON(w, http.StatusOK, map[string]any{
		"summary": map[string]int{
			"projects": sm.Projects, "apps": sm.Apps, "healthy": sm.Healthy, "needing_attention": sm.Failing, "stopped": sm.Stopped,
		},
		"needs_attention": attention,
		"projects":        out,
	})
}

// GET /api/v1/projects
func (s *Server) handleAPIProjects(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFromContext(r.Context())
	projects, err := s.store.ListProjects(user.ID)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	type item struct {
		Slug     string `json:"slug"`
		Name     string `json:"name"`
		Apps     int    `json:"apps"`
		Services int    `json:"services"`
	}
	out := []item{}
	for _, p := range projects {
		apps, _ := s.store.ListApps(p.ID)
		svcs, _ := s.store.ListServices(p.ID)
		out = append(out, item{Slug: p.Slug, Name: p.Name, Apps: len(apps), Services: len(svcs)})
	}
	apiJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// GET /api/v1/projects/{slug}
func (s *Server) handleAPIProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.apiProject(w, r)
	if !ok {
		return
	}
	apps, _ := s.store.ListApps(p.ID)
	svcs, _ := s.store.ListServices(p.ID)
	rows := s.appRows(apps, time.Now())
	applyResourceHealth(rows, svcs, s.exclusionsFor(apps))
	as := []apiAppJSON{}
	for _, row := range rows {
		as = append(as, s.toAPIApp(r, row, p.Name))
	}
	ss := []apiServiceJSON{}
	for _, sv := range svcs {
		ss = append(ss, toAPIService(sv, p.Name, s.consumers(sv)))
	}
	apiJSON(w, http.StatusOK, map[string]any{"slug": p.Slug, "name": p.Name, "apps": as, "services": ss})
}

// GET /api/v1/apps/{slug} — everything about one app except secret values.
func (s *Server) handleAPIApp(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	project, _ := s.store.GetProjectByID(app.ProjectID)
	svcs, _ := s.store.ListServices(app.ProjectID)
	rows := s.appRows([]store.App{app}, time.Now())
	applyResourceHealth(rows, svcs, s.exclusionsFor([]store.App{app}))
	row := rows[0]

	sent, notSent := appServices(app, svcs, s.exclusionsFor([]store.App{app})[app.ID])
	names := func(list []store.Service) []map[string]string {
		out := []map[string]string{}
		for _, sv := range list {
			out = append(out, map[string]string{"slug": sv.Slug, "type": sv.Type, "status": sv.Status, "env_var": envKeyFor(sv.Type)})
		}
		return out
	}
	// Variable NAMES and whether they are masked — never values.
	vars := []map[string]any{}
	if evs, err := s.store.ListEnvVars(app.ID); err == nil {
		for _, e := range evs {
			vars = append(vars, map[string]any{"key": e.Key, "secret": e.IsSecret})
		}
	}
	doms := []map[string]string{}
	if ds, err := s.store.ListDomains(app.ID); err == nil {
		for _, d := range ds {
			doms = append(doms, map[string]string{"hostname": d.Hostname, "tls_status": d.TLSStatus, "cert_expires_at": d.CertExpiresAt})
		}
	}
	deps, _ := s.store.ListDeployments(app.ID, 40)
	out := map[string]any{
		"app":         s.toAPIApp(r, row, project.Name),
		"image":       app.Image,
		"port":        app.Port,
		"replicas":    app.Replicas,
		"health_path": app.HealthPath,
		"git":         s.apiGit(app),
		"services":    map[string]any{"sent": names(sent), "not_sent": names(notSent)},
		"variables":   vars,
		"domains":     doms,
		"env_pending": s.envPending(app, deps),
	}
	if len(deps) > 0 && deps[0].Status == "failed" {
		out["explanation"] = apiExplain(deps[0].Error)
	}
	apiJSON(w, http.StatusOK, out)
}

// apiGit describes the connected repo without any secret: the repository, the
// branch, and the mode — not the deploy key, token or webhook secret.
func (s *Server) apiGit(app store.App) map[string]any {
	if app.GitSourceID == "" {
		return nil
	}
	gs, err := s.store.GetGitSource(app.GitSourceID)
	if err != nil {
		return nil
	}
	return map[string]any{
		"repository": gs.RepoURL, "provider": gs.Provider, "branch": gs.DefaultBranch,
		"workflow": app.WorkflowPath, "artifact": app.ArtifactName, "has_github_token": gs.APITokenEnc != "",
	}
}

func apiExplain(errText string) any {
	ex := builder.Explain(errText)
	if ex == nil {
		return nil
	}
	hints := []string{}
	for _, h := range ex.Hints {
		hints = append(hints, h.Text)
	}
	return map[string]any{"title": ex.Title, "hints": hints}
}

// GET /api/v1/apps/{slug}/deployments?limit=
func (s *Server) handleAPIAppDeployments(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	ds, err := s.store.ListDeployments(app.ID, apiInt(r, "limit", 20, 1, 100))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := []apiDeploymentJSON{}
	for _, d := range ds {
		out = append(out, toAPIDeployment(d, app.Slug))
	}
	apiJSON(w, http.StatusOK, map[string]any{"deployments": out})
}

// GET /api/v1/apps/{slug}/activity?limit= — the app's history in plain words.
func (s *Server) handleAPIAppActivity(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	evs, err := s.store.ListEvents(app.ID, apiInt(r, "limit", 30, 1, 200))
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	type item struct {
		At     string `json:"at"`
		Title  string `json:"title"`
		Detail string `json:"detail,omitempty"`
	}
	out := []item{}
	for _, e := range evs {
		title, _, _ := describeEvent(e.Kind)
		out = append(out, item{At: e.TS, Title: title, Detail: clipText(e.Data, 300)})
	}
	apiJSON(w, http.StatusOK, map[string]any{"activity": out})
}

// GET /api/v1/apps/{slug}/logs?tail= — the running containers' recent output.
func (s *Server) handleAPIAppLogs(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	tail := apiInt(r, "tail", 100, 1, 500)
	type slotLogs struct {
		Replica int      `json:"replica"`
		State   string   `json:"state,omitempty"`
		Lines   []string `json:"lines"`
	}
	out := []slotLogs{}
	if s.rt == nil {
		apiJSON(w, http.StatusOK, map[string]any{"logs": out})
		return
	}
	for _, sl := range s.appSlots(app) {
		summary, lines := runtime.Evidence(r.Context(), s.rt, sl.Name, tail)
		out = append(out, slotLogs{Replica: sl.Slot, State: summary, Lines: lines})
	}
	apiJSON(w, http.StatusOK, map[string]any{"logs": out})
}

// GET /api/v1/deployments/{id} — the deployment, why it failed, and whether
// retrying it is possible right now.
func (s *Server) handleAPIDeployment(w http.ResponseWriter, r *http.Request) {
	d, app, ok := s.apiDeployment(w, r)
	if !ok {
		return
	}
	out := map[string]any{"deployment": toAPIDeployment(d, app.Slug)}
	if d.Status == "failed" {
		out["explanation"] = apiExplain(d.Error)
		if ds, err := s.store.ListDeployments(app.ID, 1); err == nil && len(ds) == 1 && ds[0].ID == d.ID {
			out["can_retry"] = true
		}
	}
	apiJSON(w, http.StatusOK, out)
}

// GET /api/v1/deployments/{id}/log?tail= — the build and deploy log.
func (s *Server) handleAPIDeploymentLog(w http.ResponseWriter, r *http.Request) {
	d, _, ok := s.apiDeployment(w, r)
	if !ok {
		return
	}
	lines, err := s.store.ListBuildLogs(d.ID, 0)
	if err != nil {
		apiErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	tail := apiInt(r, "tail", 200, 1, 2000)
	truncated := false
	if len(lines) > tail {
		lines, truncated = lines[len(lines)-tail:], true
	}
	for i := range lines {
		lines[i] = clipText(strings.TrimRight(lines[i], "\r\n"), 500)
	}
	apiJSON(w, http.StatusOK, map[string]any{"status": d.Status, "truncated": truncated, "lines": lines})
}

// GET /api/v1/services/{slug} — never the connection string or credentials.
func (s *Server) handleAPIService(w http.ResponseWriter, r *http.Request) {
	sv, ok := s.apiService(w, r)
	if !ok {
		return
	}
	project, _ := s.store.GetProjectByID(sv.ProjectID)
	apiJSON(w, http.StatusOK, toAPIService(sv, project.Name, s.consumers(sv)))
}
