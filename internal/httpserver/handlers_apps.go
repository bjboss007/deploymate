package httpserver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sort"
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

// deployPort resolves the deploy form's port field: an empty field keeps
// the app's stored port (a bare 0 would persist a portless app and drop
// the preview binding); a submitted value wins.
func deployPort(form string, stored int) int {
	if p := strings.TrimSpace(form); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	return stored
}

// appSpec assembles the container spec for an image app: full env, labels,
// preview port binding, limits, and domain routing labels — the single
// source of truth shared by deploys and the start-time binding heal.
func (s *Server) appSpec(app store.App, image string, port int) runtime.Spec {
	// PORT env + limits, same conventions as the worker's runContainer.
	env := s.AppEnv(app)
	env = append(env, fmt.Sprintf("PORT=%d", effectivePort(port)))
	spec := runtime.Spec{
		Name:    dmContainerName(app.Slug),
		Image:   image,
		Env:     env,
		Labels:  dmLabels(app.Slug),
		Network: NetworkName,
	}
	if port > 0 {
		spec.Labels["deploymate.port"] = strconv.Itoa(port)
		spec.Port = port
		spec.HostPort = runtime.PreviewPort(app.Slug)
	}
	if app.MemLimitMB > 0 {
		spec.MemLimitMB = int64(app.MemLimitMB)
	}
	if app.CPULimit > 0 {
		spec.CPULimit = app.CPULimit
	}
	for k, v := range s.domainLabels(app) {
		spec.Labels[k] = v
	}
	return spec
}

// healthReasonFor explains an unhealthy badge: the monitor's probe fails,
// and the container state tells whether that looks like a crash loop
// (restarts) or an up-but-unreachable container.
func healthReasonFor(info runtime.Info) string {
	if !info.Running {
		return "container is not running — start it or check the log panel"
	}
	if info.Restarts > 0 {
		return fmt.Sprintf("container is crash-looping — %d restarts, see the log panel below for the error", info.Restarts)
	}
	return "container is running but failing health probes on its preview port — check the log panel, restart, or redeploy"
}

// startApp starts the app's container, healing a container that was
// created without its preview port binding (older deploys, manual docker
// runs) by recreating it from the current spec so the health probe can
// reach it.
func (s *Server) startApp(ctx context.Context, app store.App) error {
	name := dmContainerName(app.Slug)
	if err := s.rt.Start(ctx, name); err != nil {
		return err
	}
	if app.Port <= 0 {
		return nil
	}
	_, err := s.ensureBinding(ctx, app, name)
	return err
}

// HealApp is the monitor's auto-heal entrypoint: it ensures a running
// app's container has its preview port binding, recreating it from the
// shared spec when missing — the same heal a manual restart performs,
// minus the human. It reports whether a recreation happened so the
// monitor can record the heal. Image apps recreate from app.Image;
// git-source apps have none, so their container's own (worker-built)
// image is used — the ground truth of what is deployed.
func (s *Server) HealApp(ctx context.Context, app store.App) (bool, error) {
	if app.Port <= 0 {
		return false, nil
	}
	name := dmContainerName(app.Slug)
	if err := s.rt.Start(ctx, name); err != nil {
		return false, err
	}
	return s.ensureBinding(ctx, app, name)
}

// ensureBinding inspects the app's container and, when it is running
// without any published port, recreates it from the spec. Returns whether
// a recreation happened.
func (s *Server) ensureBinding(ctx context.Context, app store.App, name string) (bool, error) {
	info, err := s.rt.Inspect(ctx, name)
	if err != nil {
		return false, err
	}
	if len(info.PublishedPorts) > 0 {
		return false, nil
	}
	image := app.Image
	if image == "" {
		image = info.Image // git-source apps: the worker-built image
	}
	if image == "" {
		return false, fmt.Errorf("no image to recreate %s from", name)
	}
	slog.Warn("apps: container has no published port; recreating from spec", "app", app.Slug, "port", app.Port, "image", image)
	_ = s.rt.Stop(ctx, name, 5)
	if err := s.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return false, err
	}
	if _, err := s.rt.Create(ctx, s.appSpec(app, image, app.Port)); err != nil {
		return false, err
	}
	return true, s.rt.Start(ctx, name)
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
		Status: "stopped", BuildType: "dockerfile", Port: 8080,
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
	// Best-effort auto-DNS: the preview CNAME lets Cloudflare's free plan
	// issue a per-app edge cert. Single attempt, 5s bound (unlike alerts,
	// a retry buys little — the record can be created manually), and it
	// never fails app creation.
	if s.dns != nil && s.previewHost != "" {
		host := app.Slug + "." + s.previewHost
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		err := s.dns.EnsurePreviewRecord(ctx, host)
		cancel()
		if err != nil {
			slog.Warn("apps: preview dns", "app", app.Slug, "host", host, "err", err)
			_ = s.store.RecordEvent(app.ID, store.EventDNSRecordFailed, host+": "+err.Error())
			http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("App created — but its preview DNS record could not be created automatically; the preview URL will not get a certificate until the record exists."), http.StatusSeeOther)
			return
		}
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
	// container state tells the story (crash loop count vs unreachable).
	healthReason := ""
	if app.Health == "unhealthy" {
		if info, err := s.rt.Inspect(r.Context(), dmContainerName(app.Slug)); err == nil {
			healthReason = healthReasonFor(info)
		} else {
			healthReason = "container missing — redeploy the app"
		}
	}
	// Commit links for the deployments table.
	commitURLs := make(map[string]string)
	if app.GitSourceID != "" {
		if gs, err := s.store.GetGitSource(app.GitSourceID); err == nil {
			for _, d := range deployments {
				if d.CommitSHA != "" {
					commitURLs[d.ID] = gitpkg.CommitURL(gs.RepoURL, d.CommitSHA)
				}
			}
		}
	}
	render(w, r, http.StatusOK, templates.AppPage(s.viewCtx(r), project, app, deployments, envVars, git, domains, s.leMode, uptime, s.previewURL(r, app), healthReason, commitURLs))
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
	port := deployPort(r.FormValue("port"), app.Port)
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
	if _, err := s.rt.Create(ctx, s.appSpec(app, image, port)); err != nil {
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
	// Fresh container with its binding — declare healthy now (the monitor
	// corrects within 90s if the new build actually fails to serve).
	_ = s.store.UpdateAppHealth(app.ID, "healthy")
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
	// Health is meaningless while stopped; clear it so a stale
	// "unhealthy" badge can't sit next to the stopped status.
	_ = s.store.UpdateAppHealth(app.ID, "")
	_ = s.store.RecordEvent(app.ID, store.EventAppStopped, "app stopped from the dashboard")
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
	if err := s.startApp(r.Context(), app); err != nil {
		if errors.Is(err, runtime.ErrContainerNotFound) {
			http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Container is gone — deploy it again."), http.StatusSeeOther)
			return
		}
		slog.Error("apps: start", "err", err)
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Start failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.UpdateAppStatus(app.ID, "running")
	// The container is up and (re)bound — declare it healthy now rather
	// than leaving a stale "unhealthy" badge until the monitor's first
	// two OK probes land (~60s). The monitor corrects within 90s if the
	// app actually fails to serve.
	_ = s.store.UpdateAppHealth(app.ID, "healthy")
	_ = s.store.RecordEvent(app.ID, store.EventAppStarted, "app started from the dashboard")
	if r.Header.Get("HX-Request") == "true" {
		app.Status = "running"
		render(w, r, http.StatusOK, templates.AppHeadActions(s.viewCtx(r), app))
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
}

// handleAppRestart restarts an app's container in one click.
func (s *Server) handleAppRestart(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	name := dmContainerName(app.Slug)
	_ = s.rt.Stop(ctx, name, 10)
	if err := s.startApp(ctx, app); err != nil {
		if errors.Is(err, runtime.ErrContainerNotFound) {
			http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Container is gone — deploy it again."), http.StatusSeeOther)
			return
		}
		slog.Error("apps: restart", "err", err)
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Restart failed: "+err.Error()), http.StatusSeeOther)
		return
	}
	_ = s.store.UpdateAppStatus(app.ID, "running")
	_ = s.store.UpdateAppHealth(app.ID, "healthy")
	_ = s.store.RecordEvent(app.ID, store.EventAppRestarted, "app restarted from the dashboard")
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
				// The container is stopped or gone. If it still exists
				// (stopped, not removed), snapshot its last output — docker
				// logs works on a stopped container — so the panel shows why
				// the app went down instead of a blank "waiting". A removed
				// container (err != nil, e.g. between deploys) has none. This
				// tail may overlap the follow tail below if the same
				// container is then started while the panel stays open.
				if err == nil {
					if snap, serr := s.rt.Logs(ctx, name, false, 200); serr == nil {
						writeContainerLogs(w, snap)
						snap.Close()
					}
				}
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

		writeContainerLogs(w, rc)
		rc.Close()

		// The container exited or was recreated; loop back and re-inspect.
		if ctx.Err() != nil {
			return
		}
		emitted = false
	}
}

// writeContainerLogs demuxes a docker log stream (stdout/stderr multiplexed
// by stdcopy) and writes each line to the SSE response, tagging stderr. It
// blocks until the reader is exhausted: a follow stream ends when its reader
// is closed (on disconnect), a snapshot ends at EOF.
func writeContainerLogs(w http.ResponseWriter, rc io.Reader) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(stdoutW, stderrW, rc)
		stdoutW.Close()
		stderrW.Close()
	}()

	var wg sync.WaitGroup
	var mu sync.Mutex
	scan := func(rd io.Reader, prefix string) {
		defer wg.Done()
		sc := bufio.NewScanner(rd)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			mu.Lock()
			_ = sse.WriteEvent(w, sse.Event{Name: "log", Data: prefix + sc.Text()})
			mu.Unlock()
		}
	}
	wg.Add(2)
	go scan(stdoutR, "")
	go scan(stderrR, "[stderr] ")
	wg.Wait()
}

// appEnv builds the container environment for an app: connection URLs for
// every running service in its project, then the app's own variables (which
// win on duplicate keys).
// AppEnv builds the container environment: connection URLs for every
// running service in its project, then the app's own variables (which win
// on duplicate keys) — and finally ${KEY} references resolved against the
// full set, so any injected URL can be aliased to any key
// (e.g. DATABASE_URL = ${MYSQL_URL}).
func (s *Server) AppEnv(app store.App) []string {
	base := map[string]string{}

	project, err := s.store.GetProjectByID(app.ProjectID)
	if err == nil {
		svcs, err := s.store.ListServices(project.ID)
		if err == nil {
			for _, svc := range svcs {
				if svc.Status != "running" || svc.Environment != app.Environment {
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
				base[tpl.URLEnv] = tpl.ConnURL(creds, services.ContainerName(svc.Slug))
			}
		}
	}

	vars, err := s.store.ListEnvVars(app.ID)
	if err != nil {
		slog.Error("apps: list env vars", "err", err)
	} else {
		for _, v := range vars {
			val, err := crypto.Decrypt(s.encKey, v.ValueEnc)
			if err != nil {
				slog.Error("apps: decrypt env var", "key", v.Key, "err", err)
				continue
			}
			base[v.Key] = val
		}
	}

	expanded := expandRefs(base, 5)
	keys := make([]string, 0, len(expanded))
	for k := range expanded {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+expanded[k])
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

// effectivePort is the platform port convention: apps read $PORT at
// runtime, and when none is set the default is 8080 (Spring Boot/Heroku
// convention).
func effectivePort(port int) int {
	if port <= 0 {
		return 8080
	}
	return port
}
