package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// The provision tier: create and configure. There is deliberately no endpoint
// that deletes anything, and none that returns a secret: variables are
// write-only, and the repository's webhook secret and the GitHub token stay in
// the dashboard.

// created answers an accepted creation (201) and audits it.
func (s *Server) created(w http.ResponseWriter, r *http.Request, action, target, detail, appID string, body map[string]any) {
	s.audit(r, action, target, detail, "ok", appID)
	body["ok"] = true
	apiJSON(w, http.StatusCreated, body)
}

// POST /api/v1/projects {name}
func (s *Server) handleAPIProjectCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	user, _ := auth.UserFromContext(r.Context())
	p, refusal, err := s.createProjectCore(user.ID, in.Name)
	if err != nil {
		s.actionFailed(w, "create_project", err)
		return
	}
	if refusal != "" {
		s.actionRefused(w, r, "create_project", in.Name, refusal, "")
		return
	}
	s.created(w, r, "create_project", p.Slug, "", "", map[string]any{"slug": p.Slug, "name": p.Name})
}

// POST /api/v1/projects/{slug}/apps {name, environment?}
func (s *Server) handleAPIAppCreate(w http.ResponseWriter, r *http.Request) {
	project, ok := s.apiProject(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Environment string `json:"environment"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	app, warning, refusal, err := s.createAppCore(r.Context(), project, in.Name, in.Environment)
	if err != nil {
		s.actionFailed(w, "create_app", err)
		return
	}
	if refusal != "" {
		s.actionRefused(w, r, "create_app", in.Name, refusal, "")
		return
	}
	out := map[string]any{"slug": app.Slug, "name": app.Name, "project": project.Slug, "environment": app.Environment}
	if warning != "" {
		out["warning"] = warning
	}
	s.created(w, r, "create_app", app.Slug, "in "+project.Slug, app.ID, out)
}

// POST /api/v1/projects/{slug}/services {name, type, environment?}
func (s *Server) handleAPIServiceCreate(w http.ResponseWriter, r *http.Request) {
	project, ok := s.apiProject(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Type        string `json:"type"`
		Environment string `json:"environment"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	svc, refusal, err := s.createServiceCore(project, in.Name, in.Type, in.Environment)
	if err != nil {
		s.actionFailed(w, "create_service", err)
		return
	}
	if refusal != "" {
		s.actionRefused(w, r, "create_service", in.Name, refusal, "")
		return
	}
	s.created(w, r, "create_service", svc.Slug, svc.Type+" in "+project.Slug, "", map[string]any{
		"slug": svc.Slug, "type": svc.Type, "environment": svc.Environment, "status": svc.Status,
		"next": "start it with start_service; apps in the same project and environment then receive its connection URL on their next deploy",
	})
}

// POST /api/v1/services/{slug}/start
func (s *Server) handleAPIServiceStart(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.apiService(w, r)
	if !ok {
		return
	}
	if s.prov == nil {
		apiErr(w, http.StatusServiceUnavailable, "provisioning is not available on this server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.prov.Provision(ctx, svc); err != nil {
		s.audit(r, "start_service", svc.Slug, err.Error(), "refused", "")
		apiErr(w, http.StatusBadGateway, "Start failed: "+err.Error())
		return
	}
	s.audit(r, "start_service", svc.Slug, "", "ok", "")
	apiJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "running"})
}

// PUT /api/v1/apps/{slug}/variables {variables:{K:V}, secret:[K…]} — write-only.
func (s *Server) handleAPIVariablesSet(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	var in struct {
		Variables map[string]string `json:"variables"`
		Secret    []string          `json:"secret"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	if len(in.Variables) == 0 {
		s.actionRefused(w, r, "set_variables", app.Slug, "Send at least one variable in \"variables\".", app.ID)
		return
	}
	if len(in.Variables) > maxBulkEnvLines {
		s.actionRefused(w, r, "set_variables", app.Slug, fmt.Sprintf("More than %d variables in one call.", maxBulkEnvLines), app.ID)
		return
	}
	secret := map[string]bool{}
	for _, k := range in.Secret {
		secret[k] = true
	}
	items := make([]envItem, 0, len(in.Variables))
	var bad []string
	for k, v := range in.Variables {
		switch {
		case !envKeyRe.MatchString(k) || len(k) > 128:
			bad = append(bad, fmt.Sprintf("%q is not a valid name (letters, digits, underscores; must start with a letter or underscore)", truncate(k, 40)))
		case len(v) > maxEnvValueLen:
			bad = append(bad, "the value of "+k+" is too long")
		default:
			items = append(items, envItem{Key: k, Value: v, Secret: secret[k]})
		}
	}
	if len(bad) > 0 {
		if len(bad) > 3 {
			bad = bad[:3]
		}
		s.actionRefused(w, r, "set_variables", app.Slug, "Nothing saved — "+strings.Join(bad, "; ")+".", app.ID)
		return
	}
	names, added, updated, err := s.upsertEnvItems(app, items)
	if err != nil {
		s.actionFailed(w, "set_variables", err)
		return
	}
	s.audit(r, "set_variables", app.Slug, strings.Join(names, ", "), "ok", "") // names only; upsertEnvItems wrote the app event
	apiJSON(w, http.StatusOK, map[string]any{
		"ok": true, "set": names, "new": added, "updated": updated, "redeploy_needed": true,
		"note": "values are write-only and never returned; names that look sensitive are stored masked. Redeploy to apply.",
	})
}

// POST /api/v1/apps/{slug}/git {repository, provider?, branch?}
func (s *Server) handleAPIConnectRepo(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	var in struct {
		Repository string `json:"repository"`
		Provider   string `json:"provider"`
		Branch     string `json:"branch"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	if in.Provider == "" {
		in.Provider = "github"
	}
	gs, pub, refusal, err := s.connectRepoCore(app, in.Repository, in.Provider, in.Branch)
	if err != nil {
		s.actionFailed(w, "connect_repository", err)
		return
	}
	if refusal != "" {
		s.actionRefused(w, r, "connect_repository", app.Slug, refusal, app.ID)
		return
	}
	s.created(w, r, "connect_repository", app.Slug, gs.RepoURL, app.ID, map[string]any{
		"repository": gs.RepoURL, "branch": gs.DefaultBranch,
		"deploy_key":   pub, // public half only
		"webhook_path": "/hooks/" + gs.ID,
		"next":         "add the deploy key to the repository (read-only) if it is private; the webhook secret is shown in the dashboard only",
	})
}

// POST /api/v1/apps/{slug}/domains {hostname}
func (s *Server) handleAPIDomainAdd(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	var in struct {
		Hostname string `json:"hostname"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	d, refusal, err := s.addDomainCore(app, in.Hostname)
	if err != nil {
		s.actionFailed(w, "add_domain", err)
		return
	}
	if refusal != "" {
		s.actionRefused(w, r, "add_domain", app.Slug, refusal, app.ID)
		return
	}
	s.created(w, r, "add_domain", app.Slug, d.Hostname, app.ID, map[string]any{
		"hostname": d.Hostname, "next": "point the domain's DNS at this server, then redeploy the app to route it and issue its certificate",
	})
}

// PATCH /api/v1/apps/{slug}/config {image?, port?} — what an image-based app runs.
func (s *Server) handleAPIAppConfig(w http.ResponseWriter, r *http.Request) {
	app, ok := s.apiApp(w, r)
	if !ok {
		return
	}
	var in struct {
		Image         *string `json:"image"`
		Port          *int    `json:"port"`
		RootDirectory *string `json:"root_directory"`
	}
	if !apiBody(w, r, &in) {
		return
	}
	image, port := app.Image, app.Port
	if in.Image != nil {
		image = strings.TrimSpace(*in.Image)
		if image == "" || len(image) > 255 || strings.ContainsAny(image, " \t\r\n") {
			s.actionRefused(w, r, "configure_app", app.Slug, "That is not a valid image reference (for example nginx:1.27 or ghcr.io/acme/web:1.2).", app.ID)
			return
		}
	}
	if in.Port != nil {
		port = *in.Port
		if port < 1 || port > 65535 {
			s.actionRefused(w, r, "configure_app", app.Slug, "The port must be between 1 and 65535.", app.ID)
			return
		}
	}
	if in.Image == nil && in.Port == nil && in.RootDirectory == nil {
		s.actionRefused(w, r, "configure_app", app.Slug, "Send an image, a port and/or a root_directory.", app.ID)
		return
	}
	if in.RootDirectory != nil {
		dir, why := cleanRootDirectory(*in.RootDirectory)
		if why != "" {
			s.actionRefused(w, r, "configure_app", app.Slug, why, app.ID)
			return
		}
		if err := s.store.UpdateAppRootDirectory(app.ID, dir); err != nil {
			s.actionFailed(w, "configure_app", err)
			return
		}
		_ = s.store.RecordEvent(app.ID, store.EventRootDirChanged, "build folder set to "+orRoot(dir))
	}
	if err := s.store.UpdateAppDeployConfig(app.ID, image, port, app.Entrypoint, app.Command); err != nil {
		s.actionFailed(w, "configure_app", err)
		return
	}
	s.audit(r, "configure_app", app.Slug, fmt.Sprintf("image %s, port %d", image, port), "ok", app.ID)
	apiJSON(w, http.StatusOK, map[string]any{"ok": true, "image": image, "port": port, "next": "deploy_app to run it"})
}

func orRoot(dir string) string {
	if dir == "" {
		return "the repository root"
	}
	return dir
}
