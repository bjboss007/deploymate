// Package jobs runs the deployment worker: a single in-process loop that
// claims queued deployments and drives them through
// queued → building → running/failed. One worker means builds serialize and
// BuildKit never contends with itself on the host.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/alerts"
	"github.com/habibmuhammad/deploymate/internal/builder"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/proxy"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// imageRetention is how many built images per app are kept for rollbacks.
const imageRetention = 5

// Worker processes the deployment queue.
type Worker struct {
	store        *store.Store
	rt           runtime.Runtime
	prov         *services.Provisioner
	events       *sse.Broker
	encKey       [32]byte
	dataDir      string
	network      string
	leMode       string
	railpackPath string
	buildEnv     func(app store.App) []string // injected by the server (services + env vars)
	alerts       *alerts.Dispatcher
}

// NewWorker builds a Worker. buildEnv supplies the app container environment
// (shared with the manual-deploy path).
func NewWorker(st *store.Store, rt runtime.Runtime, prov *services.Provisioner, events *sse.Broker, encKey [32]byte, dataDir, network, leMode, railpackPath string, buildEnv func(store.App) []string, a *alerts.Dispatcher) *Worker {
	return &Worker{store: st, rt: rt, prov: prov, events: events, encKey: encKey, dataDir: dataDir, network: network, leMode: leMode, railpackPath: railpackPath, buildEnv: buildEnv, alerts: a}
}

// Run polls the queue until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	slog.Info("deployment worker started")

	// Reap anything left mid-build by a previous (killed) process.
	if n, err := w.store.FailStaleBuilding(); err != nil {
		slog.Error("worker: reap stale builds", "err", err)
	} else if n > 0 {
		slog.Info("worker: failed stale in-flight builds", "count", n)
	}

	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("deployment worker stopped")
			return
		case <-t.C:
			d, err := w.store.ClaimNextQueued()
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				slog.Error("worker: claim", "err", err)
				continue
			}
			// Process synchronously — this serializes builds by construction.
			w.process(ctx, d)
		}
	}
}

func (w *Worker) process(ctx context.Context, d store.Deployment) {
	app, err := w.store.GetAppByID(d.AppID)
	if err != nil {
		w.fail(d, fmt.Errorf("app lookup: %w", err))
		return
	}
	topic := "deploy:" + app.Slug
	w.publish(topic, "deploy", fmt.Sprintf("deployment %s started (%s)", shortID(d.ID), d.Kind))

	switch d.Kind {
	case "rollback", "resize":
		err = w.runRollback(ctx, app, d)
	default:
		err = w.runGitDeploy(ctx, app, d)
	}
	if err != nil {
		w.fail(d, err)
		w.publish(topic, "deploy", "failed: "+err.Error())
		w.alerts.Notify(alerts.EventDeployFailed,
			fmt.Sprintf("deploy failed: %s", app.Name),
			fmt.Sprintf("commit %s (deployment %s): %s", shortCommit(d.CommitSHA), shortID(d.ID), err.Error()))
		return
	}
	w.publish(topic, "deploy", "deployed ✓")
	if d.CommitSHA != "" {
		w.alerts.Notify(alerts.EventDeploySucceeded,
			fmt.Sprintf("deploy succeeded: %s", app.Name),
			fmt.Sprintf("commit %s is live (deployment %s)", shortCommit(d.CommitSHA), shortID(d.ID)))
	} else {
		w.alerts.Notify(alerts.EventDeploySucceeded,
			fmt.Sprintf("deploy succeeded: %s", app.Name),
			fmt.Sprintf("the current image is live (deployment %s)", shortID(d.ID)))
	}
}

// runGitDeploy clones, builds, and swaps in the new container.
func (w *Worker) runGitDeploy(ctx context.Context, app store.App, d store.Deployment) error {
	if app.GitSourceID == "" {
		return errors.New("no git source connected — connect a repo on the app page")
	}
	gs, err := w.store.GetGitSource(app.GitSourceID)
	if err != nil {
		return fmt.Errorf("git source: %w", err)
	}
	privateKey, err := crypto.Decrypt(w.encKey, gs.PrivateKeyEnc)
	if err != nil {
		return fmt.Errorf("decrypt deploy key: %w", err)
	}

	checkoutDir := filepath.Join(w.dataDir, "repos", d.ID)
	defer func() { _ = removeTree(checkoutDir) }()

	w.log(d, "system", "cloning "+gs.RepoURL+" ("+gs.DefaultBranch+")")
	w.publish("deploy:"+app.Slug, "log", "cloning "+gs.RepoURL)
	if err := gitpkg.Clone(ctx, gs.RepoURL, gs.DefaultBranch, d.CommitSHA, privateKey, checkoutDir); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	sha, message, err := gitpkg.Head(ctx, checkoutDir)
	if err != nil {
		return fmt.Errorf("read HEAD: %w", err)
	}
	if d.CommitSHA == "" {
		d.CommitSHA, d.CommitMessage = sha, message
		_ = w.store.UpdateDeployment(d)
	}
	w.log(d, "system", "building commit "+sha[:12]+" — "+message)
	w.publish("deploy:"+app.Slug, "log", "building commit "+sha[:12])

	// Infra manifest: reconcile deploymate.yml services before the app
	// container is assembled, so connection URLs land in the very env
	// assembly that injects them. The app's environment selects the
	// overlay (deploymate.{env}.yml) and the service set.
	manifestDecls, err := services.LoadManifest(checkoutDir, app.RootDirectory, app.Environment)
	if err != nil {
		return fmt.Errorf("deploymate.yml: %w", err)
	}
	if len(manifestDecls) > 0 {
		prefix := "manifest"
		if app.Environment != store.EnvProduction {
			prefix = "manifest (" + app.Environment + ")"
		}
		w.log(d, "system", prefix+": "+strings.Join(services.DeclTypes(manifestDecls), ", "))
		w.publish("deploy:"+app.Slug, "log", prefix+": "+strings.Join(services.DeclTypes(manifestDecls), ", "))
		if err := w.resolveManifest(ctx, app, d, manifestDecls); err != nil {
			return err
		}
	}

	imageTag := fmt.Sprintf("deploymate/apps/%s:%s", app.Slug, d.ID)
	streamLog := func(line string) {
		w.log(d, "stdout", line)
		w.publish("deploy:"+app.Slug, "log", line)
	}

	runtimeSpec := builder.ParseRuntimeSpec(app.Runtime)
	if runtimeSpec.Key != "" {
		// Railpack: the runtime is installed from the app's own version
		// files, or pinned by us via .mise.toml.
		rt, _ := builder.RuntimeByKey(runtimeSpec.Key)
		w.log(d, "system", "railpack build with runtime "+rt.Label+
			optionalVersion(runtimeSpec.Version))
		if err := builder.BuildRailpack(ctx, w.railpackPath, checkoutDir, imageTag, runtimeSpec, streamLog); err != nil {
			return err
		}
	} else {
		// Dockerfile: the explicit, familiar path.
		if builder.DetectBuildType(checkoutDir, app.RootDirectory) != "dockerfile" {
			return errors.New("no Dockerfile found in the repo root — select a runtime on the app page (Node.js, Python, Go, …) or add a Dockerfile")
		}
		if err := builder.Build(ctx, checkoutDir, app.RootDirectory, imageTag, streamLog); err != nil {
			return err
		}
	}

	d.ImageTag = imageTag
	_ = w.store.UpdateDeployment(d)
	if _, err := w.store.CreateImage(store.Image{AppID: app.ID, Tag: imageTag, DeploymentID: d.ID}); err != nil {
		return fmt.Errorf("record image: %w", err)
	}

	if err := w.runContainer(ctx, app, d, imageTag, map[string]string{"GIT_SHA": sha}); err != nil {
		return err
	}
	return w.finish(d, app.ID)
}

// resolveManifest reconciles the declared services with the app's
// environment and reports each action in the build log and the history
// timeline. Errors fail the deployment.
func (w *Worker) resolveManifest(ctx context.Context, app store.App, d store.Deployment, decls []services.ServiceDecl) error {
	resolutions, err := w.prov.Ensure(ctx, app.ProjectID, app.Environment, decls)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	for _, res := range resolutions {
		var line string
		switch res.Action {
		case services.ActionReused:
			line = fmt.Sprintf("manifest: reusing existing %s service", res.Type)
		case services.ActionStarted:
			line = fmt.Sprintf("manifest: starting existing %s service", res.Type)
			_ = w.store.RecordEvent(app.ID, store.EventServiceStarted, "service "+res.Service.Name+" started by deploy manifest")
		case services.ActionProvisioned:
			line = fmt.Sprintf("manifest: provisioning %s (new service, %s)", res.Type, res.Service.Image)
			_ = w.store.RecordEvent(app.ID, store.EventServiceAutoProvisioned, "service "+res.Service.Name+" auto-provisioned by deploy manifest")
		case services.ActionOrphaned:
			line = fmt.Sprintf("manifest: %s no longer declared → orphan candidate (delete or keep %s on its service page)", res.Type, res.Service.Name)
			_ = w.store.RecordEvent(app.ID, store.EventServiceOrphaned, "service "+res.Service.Name+" no longer declared by manifest — flagged for review")
		}
		w.log(d, "system", line)
		w.publish("deploy:"+app.Slug, "log", line)
	}
	return nil
}

// runRollback redeploys an existing image tag without building. Also
// serves resize deployments: same swap, with the freshly detected limits.
func (w *Worker) runRollback(ctx context.Context, app store.App, d store.Deployment) error {
	if d.ImageTag == "" {
		return errors.New("target has no image tag")
	}
	verb := "rolling back to"
	if d.Kind == "resize" {
		verb = "applying resized limits, image"
	}
	w.log(d, "system", verb+" "+d.ImageTag)
	w.publish("deploy:"+app.Slug, "log", verb+" "+d.ImageTag)
	if err := w.runContainer(ctx, app, d, d.ImageTag, nil); err != nil {
		return err
	}
	return w.finish(d, app.ID)
}

// runContainer swaps the app's running container for one from imageTag:
// stop + remove the old, create + start the new with the full app env.
func (w *Worker) runContainer(ctx context.Context, app store.App, d store.Deployment, imageTag string, extraEnv map[string]string) error {
	if err := w.rt.EnsureNetwork(ctx, w.network); err != nil {
		return err
	}
	name := "dm-" + app.Slug
	_ = w.rt.Stop(ctx, name, 5)
	if err := w.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
		return fmt.Errorf("remove old container: %w", err)
	}
	env := w.buildEnv(app)
	port := app.Port
	if port == 0 {
		port = 8080 // platform convention; railpack apps read $PORT
		_ = w.store.UpdateAppPort(app.ID, port)
	}
	// The platform's routing port maps to the app's $PORT env — railpack
	// start commands (java -Dserver.port=$PORT, node server.js, …) all
	// honor it.
	env = append(env, fmt.Sprintf("PORT=%d", port))
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	labels := map[string]string{
		"deploymate.managed": "true",
		"deploymate.app":     app.Slug,
		"deploymate.deploy":  d.ID,
	}
	if port > 0 {
		labels["deploymate.port"] = fmt.Sprintf("%d", port)
	}
	// Traefik routing labels for the app's domains.
	if domains, err := w.store.ListDomains(app.ID); err == nil {
		hostnames := make([]string, 0, len(domains))
		for _, dm := range domains {
			hostnames = append(hostnames, dm.Hostname)
		}
		for k, v := range proxy.AppLabels(app.Slug, app.Port, hostnames, proxy.ResolverForLEMode(w.leMode)) {
			labels[k] = v
		}
	}
	spec := runtime.Spec{
		Name:    name,
		Image:   imageTag,
		Env:     env,
		Labels:  labels,
		Network: w.network,
	}
	if port > 0 {
		spec.Port = port
		spec.HostPort = runtime.PreviewPort(app.Slug)
	}
	if app.MemLimitMB > 0 {
		spec.MemLimitMB = int64(app.MemLimitMB)
	}
	if app.CPULimit > 0 {
		spec.CPULimit = app.CPULimit
	}
	slog.Info("worker: container spec", "app", app.Slug, "mem_limit_mb", app.MemLimitMB, "cpu", app.CPULimit, "kind", d.Kind)
	w.publish("deploy:"+app.Slug, "deploy", "starting container "+name)
	if _, err := w.rt.Create(ctx, spec); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	if err := w.rt.Start(ctx, name); err != nil {
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// finish marks the deployment running, promotes the app, and prunes old images.
func (w *Worker) finish(d store.Deployment, appID string) error {
	d.Status = "running"
	d.Error = ""
	d.FinishedAt = store.Now()
	if err := w.store.UpdateDeployment(d); err != nil {
		return err
	}
	if err := w.store.UpdateAppStatus(appID, "running"); err != nil {
		return err
	}
	if err := w.store.SetAppCurrentDeployment(appID, d.ID); err != nil {
		return err
	}
	w.pruneImages(appID)
	return nil
}

// pruneImages keeps the newest imageRetention images per app and removes the
// rest, tags included.
func (w *Worker) pruneImages(appID string) {
	imgs, err := w.store.ListImages(appID)
	if err != nil {
		slog.Error("worker: list images", "err", err)
		return
	}
	for i := imageRetention; i < len(imgs); i++ {
		if err := w.store.DeleteImage(imgs[i].ID); err != nil {
			slog.Error("worker: delete image row", "err", err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _ = w.rt.RemoveImage(ctx, imgs[i].Tag)
		cancel()
		slog.Info("worker: pruned image", "tag", imgs[i].Tag)
	}
}

func (w *Worker) fail(d store.Deployment, err error) {
	d.Status = "failed"
	d.Error = err.Error()
	d.FinishedAt = store.Now()
	if uerr := w.store.UpdateDeployment(d); uerr != nil {
		slog.Error("worker: mark failed", "err", uerr)
	}
	if app, aerr := w.store.GetAppByID(d.AppID); aerr == nil {
		_ = w.store.UpdateAppStatus(app.ID, "failed")
	}
	slog.Error("worker: deployment failed", "deployment", d.ID, "err", err)
}

func (w *Worker) log(d store.Deployment, stream, line string) {
	if err := w.store.AppendBuildLog(d.ID, stream, line); err != nil {
		slog.Error("worker: append build log", "err", err)
	}
}

func (w *Worker) publish(topic, name, data string) {
	w.events.Publish(topic, sse.Event{Name: name, Data: data})
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func optionalVersion(v string) string {
	if v == "" {
		return " (auto-detected version)"
	}
	return " pinned to " + v
}

func shortCommit(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "HEAD"
	}
	return sha
}

func removeTree(dir string) error { return os.RemoveAll(dir) }
