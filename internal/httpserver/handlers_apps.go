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

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/builder"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/githubci"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/proxy"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/swap"
	"github.com/habibmuhammad/deploymate/web/templates"
)

// NetworkName is the shared bridge all DeployMate containers live on.
const NetworkName = "deploymate-net"

// dmContainerName namespaces containers so DeployMate never collides with
// the host's other docker work.
func dmContainerName(slug string) string { return appspec.CanonicalName(slug) }

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

// appSpec assembles the container spec for one replica slot through the
// shared appspec builder: full env, labels, the slot's preview port binding,
// limits, and domain routing. Used by start-heal and ensureBinding — the
// in-place slot-name + stored-port shape (zero-downtime deploys build their
// own staged spec).
func (s *Server) appSpec(app store.App, image string, port int, slot appspec.Slot) runtime.Spec {
	env := s.AppEnv(app)
	if port <= 0 {
		// Port-less image apps still get the platform's $PORT convention.
		env = append(env, fmt.Sprintf("PORT=%d", effectivePort(port)))
	}
	// Slot 1 keeps the pre-replicas router name (the slug); extra slots own
	// their own — Traefik drops a router two containers define differently.
	router := ""
	if slot.Slot > 1 {
		router = app.Slug + "-r" + strconv.Itoa(slot.Slot)
	}
	return appspec.BuildSpec(app, appspec.Options{
		Image:      image,
		Name:       slot.Name,
		Port:       port,
		HostPort:   slot.HostPort,
		Env:        env,
		RouterName: router,
		Slot:       slot.Slot,
		Domains:    s.domainHostnames(app),
		LEResolver: proxy.ResolverForLEMode(s.leMode),
		// Start/restart/heal rebuild from the row, so the stored overrides
		// must carry through here (nil for git apps and empty fields).
		Entrypoint: appspec.SplitArgs(app.Entrypoint),
		Cmd:        appspec.SplitArgs(app.Command),
	})
}

// domainHostnames returns the app's routed hostnames for the Traefik labels.
func (s *Server) domainHostnames(app store.App) []string {
	domains, err := s.store.ListDomains(app.ID)
	if err != nil {
		slog.Error("apps: list domains for labels", "err", err)
		return nil
	}
	hostnames := make([]string, 0, len(domains))
	for _, d := range domains {
		hostnames = append(hostnames, d.Hostname)
	}
	return hostnames
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

// appSlots resolves the app's replicas (see appspec.Slots): the recorded
// slots, or a synthetic slot 1 for apps not redeployed since replicas.
func (s *Server) appSlots(app store.App) []appspec.Slot {
	rows, err := s.store.ListAppReplicas(app.ID)
	if err != nil {
		slog.Error("apps: list replicas", "app", app.Slug, "err", err)
	}
	return appspec.Slots(app, rows)
}

// startApp starts every replica, healing a container that was created
// without its preview port binding (older deploys, manual docker runs) by
// recreating it from the current spec so the health probe can reach it.
// Slot 1's error is returned (ErrContainerNotFound → "deploy it again");
// a missing extra slot is left to the monitor's heal.
func (s *Server) startApp(ctx context.Context, app store.App) error {
	for _, sl := range s.appSlots(app) {
		err := s.rt.Start(ctx, sl.Name)
		if err == nil && app.Port > 0 {
			_, err = s.ensureBinding(ctx, app, sl)
		}
		if err != nil {
			if sl.Slot == 1 {
				return err
			}
			slog.Warn("apps: start replica", "app", app.Slug, "slot", sl.Slot, "err", err)
		}
	}
	return nil
}

// stopApp stops every replica. A container that is already gone is fine.
func (s *Server) stopApp(ctx context.Context, app store.App, timeoutSec int) error {
	var first error
	for _, sl := range s.appSlots(app) {
		if err := s.rt.Stop(ctx, sl.Name, timeoutSec); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) && first == nil {
			first = err
		}
	}
	return first
}

// Heal actions HealApp reports; the monitor records them as app_healed.
const (
	HealRestarted = "restarted its stopped container"
	HealRebound   = "recreated its container to restore the preview port binding"
	HealRecreated = "recreated its missing container from slot 1's image"
)

// HealApp is the monitor's auto-heal entrypoint for one replica: a stopped
// container is started, one running without its preview port binding is
// recreated from the shared spec, and an extra slot (2..N) whose container
// is gone entirely is recreated from slot 1's image — the same heals a
// manual start/restart performs, minus the human. A missing slot 1 stays a
// human redeploy (the UI says so). It returns what it did (one of the
// Heal* constants, "" = nothing needed) so the monitor can record it.
// Image apps recreate from app.Image; git-source apps have none, so a
// container's own (worker-built) image is used — the ground truth of what
// is deployed.
func (s *Server) HealApp(ctx context.Context, app store.App, slot appspec.Slot) (string, error) {
	if app.Port <= 0 {
		return "", nil
	}
	info, err := s.rt.Inspect(ctx, slot.Name)
	if err != nil {
		if errors.Is(err, runtime.ErrContainerNotFound) && slot.Slot > 1 {
			if err := s.recreateSlot(ctx, app, slot); err != nil {
				return "", err
			}
			return HealRecreated, nil
		}
		return "", err
	}
	restarted := false
	if !info.Running {
		if err := s.rt.Start(ctx, slot.Name); err != nil {
			return "", err
		}
		restarted = true
	}
	rebound, err := s.ensureBinding(ctx, app, slot)
	switch {
	case err != nil:
		return "", err
	case rebound:
		return HealRebound, nil
	case restarted:
		return HealRestarted, nil
	}
	return "", nil
}

// recreateSlot rebuilds a vanished extra replica from slot 1's image on the
// slot's recorded port (a fresh loopback port when none was recorded).
func (s *Server) recreateSlot(ctx context.Context, app store.App, slot appspec.Slot) error {
	image := app.Image
	if image == "" {
		info, err := s.rt.Inspect(ctx, appspec.SlotName(app.Slug, 1))
		if err != nil {
			return fmt.Errorf("replica %d is gone and slot 1 has no image to copy: %w", slot.Slot, err)
		}
		image = info.Image
	}
	if slot.HostPort == 0 {
		port, err := swap.ReserveLoopbackPort()
		if err != nil {
			return err
		}
		slot.HostPort = port
	}
	slog.Warn("apps: replica container missing; recreating", "app", app.Slug, "slot", slot.Slot, "image", image)
	if _, err := s.rt.Create(ctx, s.appSpec(app, image, app.Port, slot)); err != nil {
		return err
	}
	if err := s.rt.Start(ctx, slot.Name); err != nil {
		return err
	}
	return s.store.UpsertAppReplica(store.AppReplica{
		AppID: app.ID, Slot: slot.Slot, ContainerName: slot.Name, HostPort: slot.HostPort, DeployID: slot.DeployID,
	})
}

// ensureBinding inspects a replica's container and, when it is running
// without any published port, recreates it from the spec. Returns whether
// a recreation happened.
func (s *Server) ensureBinding(ctx context.Context, app store.App, slot appspec.Slot) (bool, error) {
	name := slot.Name
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
	slog.Warn("apps: container has no published port; recreating from spec", "app", app.Slug, "slot", slot.Slot, "port", app.Port, "image", image)
	_ = s.rt.Stop(ctx, name, 5)
	if err := s.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return false, err
	}
	if _, err := s.rt.Create(ctx, s.appSpec(app, image, app.Port, slot)); err != nil {
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
				if gs.Provider == "github" {
					wf := githubci.WorkflowOpts{Branch: gs.DefaultBranch, JavaMajor: builder.JavaMajor(app.Runtime), ArtifactName: app.ArtifactName}
					git.GitHub, git.DeployMode, git.HasToken = true, app.DeployMode, gs.APITokenEnc != ""
					git.WorkflowPath, git.ArtifactName = app.WorkflowPath, app.ArtifactName
					git.WorkflowGradle = githubci.Workflow(wf)
					wf.Tool = githubci.ToolMaven
					git.WorkflowMaven = githubci.Workflow(wf)
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
	render(w, r, http.StatusOK, templates.AppPage(s.viewCtx(r), project, app, deployments, envVars, git, domains, s.leMode, uptime, s.previewURL(r, app), healthReason, commitURLs, s.replicasInfo(app)))
}

// replicasInfo builds the app page's replicas panel from the replica table.
// An app with no rows (not redeployed since replicas) shows no slot table —
// its single container is the whole story until the next deploy or scale.
func (s *Server) replicasInfo(app store.App) templates.ReplicasInfo {
	ri := templates.ReplicasInfo{
		Desired:    store.ClampReplicas(app.Replicas),
		Max:        store.MaxReplicas,
		Target:     app.CurrentDeploymentID,
		HealthPath: appspec.HealthPath(app),
	}
	rows, err := s.store.ListAppReplicas(app.ID)
	if err != nil {
		slog.Error("apps: list replicas", "app", app.Slug, "err", err)
	}
	for _, r := range rows {
		v := templates.ReplicaView{Slot: r.Slot, Name: r.ContainerName, Status: r.Status, DeployID: r.DeployID,
			OnTarget: r.DeployID == app.CurrentDeploymentID}
		if v.Status == "healthy" {
			ri.Healthy++
		}
		if v.OnTarget {
			ri.OnTarget++
		}
		ri.Slots = append(ri.Slots, v)
	}
	return ri
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

// handleAppDeploy persists the manual deploy form (image, port) and queues
// a deployment for the worker. The worker owns everything after this —
// bounded pull, staged spec, zero-downtime swap, finish/fail — the same
// path git builds take. Failures land on the deployment page instead of
// blocking the HTTP request on a hung pull.
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

	// Persist the chosen image/port/overrides first so start/restart keep
	// working and the worker builds the spec from a fresh row.
	if err := s.store.UpdateAppDeployConfig(app.ID, image, port,
		strings.TrimSpace(r.FormValue("entrypoint")), strings.TrimSpace(r.FormValue("command"))); err != nil {
		slog.Error("apps: update deploy config", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	d, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "manual", Status: "queued", Trigger: "manual", ImageTag: image,
	})
	if err != nil {
		slog.Error("apps: queue deploy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/deployments/"+d.ID, http.StatusSeeOther)
}

func (s *Server) handleAppStop(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if err := s.stopApp(r.Context(), app, 10); err != nil {
		slog.Error("apps: stop", "err", err)
		redirectOrHX(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Stop failed: "+err.Error()))
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
			redirectOrHX(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Container is gone — deploy it again."))
			return
		}
		slog.Error("apps: start", "err", err)
		redirectOrHX(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Start failed: "+err.Error()))
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
	_ = s.stopApp(ctx, app, 10)
	if err := s.startApp(ctx, app); err != nil {
		if errors.Is(err, runtime.ErrContainerNotFound) {
			redirectOrHX(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Container is gone — deploy it again."))
			return
		}
		slog.Error("apps: restart", "err", err)
		redirectOrHX(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Restart failed: "+err.Error()))
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
	// Every replica goes, plus any slot container the table does not know
	// about (a crashed rollout's leftovers above the recorded set).
	names := map[string]bool{}
	for _, sl := range s.appSlots(app) {
		names[sl.Name] = true
	}
	for slot := 1; slot <= store.MaxReplicas; slot++ {
		names[appspec.SlotName(app.Slug, slot)] = true
	}
	for name := range names {
		_ = s.rt.Stop(ctx, name, 5)
		if err := s.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
			slog.Error("apps: remove container", "container", name, "err", err)
		}
	}
	if err := s.store.DeleteApp(app.ID); err != nil {
		slog.Error("apps: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Best-effort cleanup of the preview CNAME auto-DNS created at app
	// create. Deletion has already happened — a failure only means the
	// record lingers (the pre-cleanup behavior) — but the flash tells the
	// human it needs removing by hand. Mirrors create's never-fail rule.
	if s.dns != nil && s.previewHost != "" {
		host := app.Slug + "." + s.previewHost
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.dns.RemovePreviewRecord(ctx, host)
		cancel()
		if err != nil {
			slog.Warn("apps: preview dns cleanup", "app", app.Slug, "host", host, "err", err)
			project, _ := s.store.GetProjectByID(app.ProjectID)
			http.Redirect(w, r, "/projects/"+project.Slug+"?flash="+flashURL("App deleted — but its preview DNS record could not be removed automatically; it will keep resolving until you delete it in Cloudflare."), http.StatusSeeOther)
			return
		}
	}
	project, _ := s.store.GetProjectByID(app.ProjectID)
	http.Redirect(w, r, "/projects/"+project.Slug, http.StatusSeeOther)
}

// handleAppLogs streams container logs to the browser over SSE. Each
// connection tails its own docker streams; closing the connection closes
// the tails. With replicas the slots' streams are merged into one SSE, each
// line prefixed with its slot ("[r2] …"); ?replica=r2 narrows the stream to
// one slot (replicas spec, decision 2). The slot set is re-resolved every
// few seconds, so scaling while the panel is open adds/removes streams.
//
// The connection NEVER closes on its own while the browser is open: if a
// container is missing or stopped, the handler sends a waiting event and
// keeps the stream alive. Closing would make the browser's EventSource
// reconnect in a tight loop — constant reload churn.
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

	// One lock serializes every write to the response: the slot streams and
	// the heartbeat all share it, so events never interleave mid-frame.
	var mu sync.Mutex
	emit := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		_ = sse.WriteEvent(w, sse.Event{Name: "log", Data: line})
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				mu.Lock()
				_, err := fmt.Fprint(w, ": ping\n\n")
				if err == nil {
					flusher.Flush()
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	// The slot set is re-resolved every logsResyncInterval, so scaling
	// while the panel is open adds/removes streams without a reload.
	want := r.URL.Query().Get("replica")
	type stream struct {
		cancel   context.CancelFunc
		prefixed bool
	}
	streams := map[int]*stream{}
	seen := map[int]bool{} // slots streamed before: re-attach with no backlog
	var wg sync.WaitGroup
	defer func() {
		for _, st := range streams {
			st.cancel()
		}
		wg.Wait()
	}()
	first, missingNoted := true, false
	resync := func() {
		all := s.appSlots(app)
		multi := len(all) > 1
		wanted := map[int]appspec.Slot{}
		for _, sl := range all {
			if want == "" || "r"+strconv.Itoa(sl.Slot) == want {
				wanted[sl.Slot] = sl
			}
		}
		if want != "" && len(wanted) == 0 && !missingNoted {
			emit("no replica " + want + " — this app runs " + strconv.Itoa(len(all)) + " replica(s)")
			missingNoted = true
		}
		changed := false
		for n, st := range streams {
			if _, ok := wanted[n]; !ok || st.prefixed != multi {
				// Removed slot, or the prefix style flipped (1 ↔ N replicas).
				st.cancel()
				delete(streams, n)
				changed = true
			}
		}
		for n, sl := range wanted {
			if streams[n] != nil {
				continue
			}
			prefix := ""
			if multi {
				prefix = "[r" + strconv.Itoa(n) + "] "
			}
			tail := 200
			if seen[n] {
				tail = 0 // already shown this slot's backlog on this connection
			}
			seen[n] = true
			sctx, cancel := context.WithCancel(ctx)
			streams[n] = &stream{cancel: cancel, prefixed: multi}
			slotEmit := func(line string) { emit(prefix + line) }
			wg.Add(1)
			go func(name string) {
				defer wg.Done()
				s.followContainerLogs(sctx, name, tail, slotEmit)
			}(sl.Name)
			changed = true
		}
		if changed && !first {
			names := make([]string, 0, len(streams))
			for n := 1; n <= store.MaxReplicas; n++ {
				if streams[n] != nil {
					names = append(names, "r"+strconv.Itoa(n))
				}
			}
			emit("— replicas changed: now streaming " + strings.Join(names, ", ") + " —")
		}
		first = false
	}
	resync()
	t := time.NewTicker(logsResyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			resync()
		}
	}
}

// logsResyncInterval is how often an open log panel re-resolves the app's
// replicas (a var so tests can shorten it).
var logsResyncInterval = 5 * time.Second

// followContainerLogs tails one container until ctx ends, re-attaching
// across restarts and recreations (deploy swaps rename a new container into
// the name). tail is the backlog for the first attach; re-attaches after a
// restart/recreate show the usual 200 lines.
func (s *Server) followContainerLogs(ctx context.Context, name string, tail int, emit func(string)) {
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
						writeContainerLogs(emit, snap)
						snap.Close()
					}
				}
				emit("waiting for the container — deploy or start the app to see logs")
				emitted = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
				continue
			}
		}

		rc, err := s.rt.Logs(ctx, name, true, tail)
		tail = 200
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

		writeContainerLogs(emit, rc)
		rc.Close()

		// The container exited or was recreated; loop back and re-inspect.
		if ctx.Err() != nil {
			return
		}
		emitted = false
	}
}

// writeContainerLogs demuxes a docker log stream (stdout/stderr multiplexed
// by stdcopy) and emits each line, tagging stderr. It blocks until the
// reader is exhausted: a follow stream ends when its reader is closed (on
// disconnect), a snapshot ends at EOF. emit must be safe for concurrent use.
func writeContainerLogs(emit func(string), rc io.Reader) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(stdoutW, stderrW, rc)
		stdoutW.Close()
		stderrW.Close()
	}()

	var wg sync.WaitGroup
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

// redirectOrHX sends the browser to a full page. The header action buttons
// (start/stop/restart) are HTMX calls that swap only #head-actions; a plain
// 303 would make HTMX follow it and paste the WHOLE page — layout and all —
// into that fragment (the nested-page glitch). HX-Redirect makes HTMX do a
// real navigation instead.
func redirectOrHX(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// effectivePort is the platform port convention: apps read $PORT at
// runtime, and when none is set the default is 8080 (Spring Boot/Heroku
// convention).
func effectivePort(port int) int {
	if port <= 0 {
		return 8080
	}
	return port
}

// handleAppReplicas sets the app's replica count (1..MaxReplicas). A running
// app with a deployed image gets a `scale` deployment queued — the worker
// starts missing slots from the current image or removes the highest ones,
// no build, no downtime. Anything else (stopped, never deployed) just
// records the count: the next deploy converges the replica set.
func (s *Server) handleAppReplicas(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := "/apps/" + app.Slug
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("replicas")))
	if err != nil || n < 1 || n > store.MaxReplicas {
		http.Redirect(w, r, back+"?flash="+flashURL(fmt.Sprintf("Replicas must be a number from 1 to %d.", store.MaxReplicas)), http.StatusSeeOther)
		return
	}
	from := store.ClampReplicas(app.Replicas)
	if err := s.store.UpdateAppReplicas(app.ID, n); err != nil {
		slog.Error("apps: update replicas", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if n == from {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	_ = s.store.RecordEvent(app.ID, store.EventAppScaled, fmt.Sprintf("replicas %d → %d", from, n))

	image := ""
	if app.CurrentDeploymentID != "" {
		if cur, err := s.store.GetDeployment(app.CurrentDeploymentID); err == nil {
			image = cur.ImageTag
		}
	}
	if image == "" {
		// Legacy rows may lack the tag; the running slot-1 container is the
		// ground truth of what is deployed.
		if info, err := s.rt.Inspect(r.Context(), appspec.SlotName(app.Slug, 1)); err == nil && info.Running {
			image = info.Image
		}
	}
	if app.Status != "running" || image == "" {
		http.Redirect(w, r, back+"?flash="+flashURL(fmt.Sprintf("Replicas set to %d — applied on the next deploy.", n)), http.StatusSeeOther)
		return
	}
	d, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "scale", Status: "queued", Trigger: "scale", ImageTag: image,
	})
	if err != nil {
		slog.Error("apps: queue scale", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/deployments/"+d.ID, http.StatusSeeOther)
}
