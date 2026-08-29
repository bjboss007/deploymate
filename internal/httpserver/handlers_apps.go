package httpserver

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// NetworkName is the shared bridge all DeployMate containers live on.
const NetworkName = "deploymate-net"

// dmContainerName namespaces containers so DeployMate never collides with
// the host's other docker work.
func dmContainerName(slug string) string { return "dm-" + slug }

// dmLabels marks containers as DeployMate-owned; P5 extends this with
// Traefik routing labels.
func dmLabels(slug string) map[string]string {
	return map[string]string{
		"deploymate.managed": "true",
		"deploymate.app":     slug,
	}
}

func (s *Server) handleAppCreate(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromRequest(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || len(name) > 64 {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("App name must be 1-64 characters."), http.StatusSeeOther)
		return
	}
	slug := slugify(name)
	if slug == "" {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("App name has no usable characters."), http.StatusSeeOther)
		return
	}
	app, err := s.store.CreateApp(store.App{
		ProjectID: project.ID, Name: name, Slug: slug,
		Status: "stopped", BuildType: "dockerfile",
	})
	if errors.Is(err, store.ErrSlugTaken) {
		http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("That name is already taken."), http.StatusSeeOther)
		return
	}
	if err != nil {
		slog.Error("apps: create", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
}

func (s *Server) handleAppPage(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		slog.Error("apps: get project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	deployments, err := s.store.ListDeployments(app.ID, 10)
	if err != nil {
		slog.Error("apps: list deployments", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	envVars, err := s.store.ListEnvVars(app.ID)
	if err != nil {
		slog.Error("apps: list env vars", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	for i := range envVars {
		if envVars[i].IsSecret {
			continue
		}
		if val, err := crypto.Decrypt(s.encKey, envVars[i].ValueEnc); err == nil {
			envVars[i].Value = val
		}
	}

	var git *templates.GitInfo
	if app.GitSourceID != "" {
		if gs, err := s.store.GetGitSource(app.GitSourceID); err == nil {
			secret, err1 := crypto.Decrypt(s.encKey, gs.WebhookSecretEnc)
			pubKey, err2 := gitpkg.PublicKeyFromPEM(mustDecrypt(s, gs.PrivateKeyEnc))
			if err1 == nil && err2 == nil {
				git = &templates.GitInfo{
					RepoURL:       gs.RepoURL,
					Provider:      gs.Provider,
					DefaultBranch: gs.DefaultBranch,
					PublicKey:     pubKey,
					WebhookPath:   "/hooks/" + gs.ID,
					WebhookSecret: secret,
				}
			}
		}
	}
	domains, err := s.store.ListDomains(app.ID)
	if err != nil {
		slog.Error("apps: list domains", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	uptime := make(map[string][]bool, len(domains))
	for _, dm := range domains {
		checks, err := s.store.ListUptimeChecks(dm.ID, 30)
		if err != nil {
			continue
		}
		dots := make([]bool, 0, len(checks))
		for _, c := range checks {
			dots = append(dots, c.OK)
		}
		uptime[dm.ID] = dots
	}
	// Unhealthy badge must say why: the monitor knows the probe fails; the
	// container state tells the story (crash loop count).
	healthReason := ""
	if app.Health == "unhealthy" {
		if info, err := s.rt.Inspect(r.Context(), dmContainerName(app.Slug)); err == nil {
			if info.Running {
				healthReason = fmt.Sprintf("container is crash-looping — %d restarts, see the log panel below for the error", info.Restarts)
			} else {
				healthReason = "container is not running — start it or check the log panel"
			}
		} else {
			healthReason = "container missing — redeploy the app"
		}
	}
	render(w, r, http.StatusOK, templates.AppPage(s.viewCtx(r), project, app, deployments, envVars, git, domains, s.leMode, uptime, previewURL(r, app), healthReason))
}

// mustDecrypt decrypts or returns "" (best-effort display helper).
func mustDecrypt(s *Server, envelope string) string {
	if envelope == "" {
		return ""
	}
	plain, err := crypto.Decrypt(s.encKey, envelope)
	if err != nil {
		slog.Error("decrypt for display", "err", err)
		return ""
	}
	return plain
}

// handleAppDeploy runs a manual container deploy: pull, replace any running
// container, start. Synchronous for now; P4 moves git builds into the
// worker with a full queued→building→running state machine.
func (s *Server) handleAppDeploy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	image := strings.TrimSpace(r.FormValue("image"))
	if image == "" {
		image = app.Image
	}
	port := 0
	if p := strings.TrimSpace(r.FormValue("port")); p != "" {
		port, _ = strconv.Atoi(p)
	}
	if image == "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Choose an image to deploy."), http.StatusSeeOther)
		return
	}

	// Persist the chosen image/port so restart/start keep working.
	if err := s.store.UpdateAppImagePort(app.ID, image, port); err != nil {
		slog.Error("apps: update image/port", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	d, err := s.store.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "manual", Status: "running", ImageTag: image})
	if err != nil {
		slog.Error("apps: create deployment", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	name := dmContainerName(app.Slug)
	fail := func(err error) {
		d.Status, d.Error = "failed", err.Error()
		_ = s.store.UpdateDeployment(d)
		_ = s.store.UpdateAppStatus(app.ID, "failed")
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Deploy failed: "+err.Error()), http.StatusSeeOther)
	}

	has, err := s.rt.HasImage(ctx, image)
	if err != nil {
		fail(err)
		return
	}
	if !has {
		s.events.Publish("app:"+app.Slug, sse.Event{Name: "deploy", Data: "pulling " + image})
		if err := s.rt.PullImage(ctx, image); err != nil {
			fail(err)
			return
		}
	}
	if err := s.rt.EnsureNetwork(ctx, NetworkName); err != nil {
		fail(err)
		return
	}

	// Replace the old container: stop + remove, then create fresh. Ignore
	// not-found (first deploy).
	_ = s.rt.Stop(ctx, name, 5)
	if err := s.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		fail(err)
		return
	}
	spec := runtime.Spec{
		Name:    name,
		Image:   image,
		Env:     s.AppEnv(app),
		Labels:  dmLabels(app.Slug),
		Network: NetworkName,
	}
	if port > 0 {
		spec.Labels["deploymate.port"] = strconv.Itoa(port)
		spec.Port = port
		spec.HostPort = runtime.PreviewPort(app.Slug)
	}
	for k, v := range s.domainLabels(app) {
		spec.Labels[k] = v
	}
	if _, err := s.rt.Create(ctx, spec); err != nil {
		fail(err)
		return
	}
	s.events.Publish("app:"+app.Slug, sse.Event{Name: "deploy", Data: "starting container"})
	if err := s.rt.Start(ctx, name); err != nil {
		fail(err)
		return
	}

	_ = s.store.UpdateDeployment(d)
	if err := s.store.UpdateAppStatus(app.ID, "running"); err != nil {
		slog.Error("apps: set running", "err", err)
	}
	s.events.Publish("app:"+app.Slug, sse.Event{Name: "deploy", Data: "deployed " + image})
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
}

func (s *Server) handleAppStop(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.rt.Stop(r.Context(), dmContainerName(app.Slug), 10); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		slog.Error("apps: stop", "err", err)
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Stop failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.UpdateAppStatus(app.ID, "stopped")
	if r.Header.Get("HX-Request") == "true" {
		app.Status = "stopped"
		render(w, r, http.StatusOK, templates.AppHeadActions(s.viewCtx(r), app))
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
}

func (s *Server) handleAppStart(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.rt.Start(r.Context(), dmContainerName(app.Slug)); err != nil {
		if errors.Is(err, runtime.ErrContainerNotFound) {
			http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Container is gone — deploy it again."), http.StatusSeeOther)
			return
		}
		slog.Error("apps: start", "err", err)
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Start failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.UpdateAppStatus(app.ID, "running")
	if r.Header.Get("HX-Request") == "true" {
		app.Status = "running"
		render(w, r, http.StatusOK, templates.AppHeadActions(s.viewCtx(r), app))
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
}

func (s *Server) handleAppDelete(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := dmContainerName(app.Slug)
	_ = s.rt.Stop(ctx, name, 5)
	if err := s.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		slog.Error("apps: remove container", "err", err)
	}
	if err := s.store.DeleteApp(app.ID); err != nil {
		slog.Error("apps: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	project, _ := s.store.GetProjectByID(app.ProjectID)
	http.Redirect(w, r, "/projects/"+project.Slug, http.StatusSeeOther)
}

// handleAppLogs streams container logs to the browser over SSE. Each
// connection tails its own docker stream; closing the connection closes the
// tail.
//
// The connection NEVER closes on its own while the browser is open: if the
// container is missing or stopped, the handler sends a waiting event every
// few seconds and keeps the stream alive. Closing would make the browser's
// EventSource reconnect in a tight loop — constant reload churn.
func (s *Server) handleAppLogs(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	sse.SetHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	name := dmContainerName(app.Slug)

	go sse.Heartbeat(ctx.Done(), w, flusher)

	emitted := false
	for {
		info, err := s.rt.Inspect(ctx, name)
		if err != nil || !info.Running {
			if !emitted {
				_ = sse.WriteEvent(w, sse.Event{Name: "log", Data: "waiting for the container — deploy or start the app to see logs"})
				emitted = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}

		rc, err := s.rt.Logs(ctx, name, true, 200)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}

		// Closing the reader cancels the follow tail; do it on disconnect.
		go func() {
			<-ctx.Done()
			rc.Close()
		}()

		stdoutR, stdoutW := io.Pipe()
		stderrR, stderrW := io.Pipe()
		go func() {
			_, _ = stdcopy.StdCopy(stdoutW, stderrW, rc)
			stdoutW.Close()
			stderrW.Close()
		}()

		var wg sync.WaitGroup
		var mu sync.Mutex
		emit := func(line string) {
			mu.Lock()
			defer mu.Unlock()
			_ = sse.WriteEvent(w, sse.Event{Name: "log", Data: line})
		}
		scan := func(rd io.Reader, prefix string) {
			defer wg.Done()
			sc := bufio.NewScanner(rd)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				emit(prefix + sc.Text())
			}
		}
		wg.Add(2)
		go scan(stdoutR, "")
		go scan(stderrR, "[stderr] ")
		wg.Wait()
		rc.Close()

		// The container exited or was recreated; loop back and re-inspect.
		if ctx.Err() != nil {
			return
		}
		emitted = false
	}
}

// appEnv builds the container environment for an app: connection URLs for
// every running service in its project, then the app's own variables (which
// win on duplicate keys).
func (s *Server) AppEnv(app store.App) []string {
	env := []string{}

	project, err := s.store.GetProjectByID(app.ProjectID)
	if err == nil {
		svcs, err := s.store.ListServices(project.ID)
		if err == nil {
			for _, svc := range svcs {
				if svc.Status != "running" {
					continue
				}
				tpl, ok := services.ForType(svc.Type)
				if !ok {
					continue
				}
				credsEnc, err := s.store.GetServiceCredentials(svc.ID)
				if err != nil || len(credsEnc) == 0 {
					continue
				}
				creds := s.decryptCreds(credsEnc)
				env = append(env, tpl.URLEnv+"="+tpl.ConnURL(creds, dmServiceName(svc.Slug)))
			}
		}
	}

	vars, err := s.store.ListEnvVars(app.ID)
	if err != nil {
		slog.Error("apps: list env vars", "err", err)
		return env
	}
	for _, v := range vars {
		val, err := crypto.Decrypt(s.encKey, v.ValueEnc)
		if err != nil {
			slog.Error("apps: decrypt env var", "key", v.Key, "err", err)
			continue
		}
		env = append(env, v.Key+"="+val)
	}
	return env
}

// --- helpers -------------------------------------------------------------

func (s *Server) projectFromRequest(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	user, _ := auth.UserFromContext(r.Context())
	project, err := s.store.GetProjectBySlug(user.ID, chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return project, false
	}
	if err != nil {
		slog.Error("project lookup", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return project, false
	}
	return project, true
}

func (s *Server) appFromRequest(w http.ResponseWriter, r *http.Request) (store.App, bool) {
	app, err := s.store.GetAppBySlug(chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return app, false
	}
	if err != nil {
		slog.Error("app lookup", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return app, false
	}
	return app, true
}

func flashURL(msg string) string { return url.QueryEscape(msg) }
