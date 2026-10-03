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
	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/builder"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/proxy"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/stack"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/swap"
)

// imageRetention is how many built images per app are kept for rollbacks.
const imageRetention = 5

// manualPullTimeout bounds a manual deploy's image pull so a hung registry
// can't hold the single worker hostage indefinitely.
const manualPullTimeout = 10 * time.Minute

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

	// githubAPI overrides the GitHub REST base URL for prebuilt deploys
	// (tests); "" = https://api.github.com.
	githubAPI string
	// buildFn builds a Docker context into an image; nil = builder.Build.
	// A seam so artifact-deploy tests do not need a Docker daemon.
	buildFn func(ctx context.Context, contextDir, rootDir, imageTag string, log func(string)) error

	// Test seams (mirroring the monitor's probeURLFn): zero values pick the
	// swap defaults (30 attempts × 2s) and manualPullTimeout.
	pullTimeout   time.Duration
	probeURL      func(hostPort int) string
	probeAttempts int
	probeInterval time.Duration
}

// NewWorker builds a Worker. buildEnv supplies the app container environment
// (shared with the manual-deploy path).
func NewWorker(st *store.Store, rt runtime.Runtime, prov *services.Provisioner, events *sse.Broker, encKey [32]byte, dataDir, network, leMode, railpackPath string, buildEnv func(store.App) []string, a *alerts.Dispatcher) *Worker {
	return &Worker{store: st, rt: rt, prov: prov, events: events, encKey: encKey, dataDir: dataDir, network: network, leMode: leMode, railpackPath: railpackPath, buildEnv: buildEnv, alerts: a, pullTimeout: manualPullTimeout}
}

// SetGitHubAPI points prebuilt deploys at a different GitHub API base URL
// (the e2e's fake GitHub). Empty keeps the default, https://api.github.com.
func (w *Worker) SetGitHubAPI(base string) { w.githubAPI = base }

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
	case "scale":
		if err := w.runScale(ctx, app, d); err != nil {
			w.fail(d, err)
			w.publish(topic, "deploy", "failed: "+err.Error())
			w.alerts.Notify(alerts.EventDeployFailed,
				fmt.Sprintf("scale failed: %s", app.Name),
				fmt.Sprintf("deployment %s: %s", shortID(d.ID), err.Error()))
			return
		}
		w.publish(topic, "deploy", "scaled ✓")
		return
	case "rollback", "resize", "redeploy":
		err = w.runRollback(ctx, app, d)
	case "manual":
		err = w.runManualDeploy(ctx, app, d)
	default:
		if app.DeployMode == store.DeployModeArtifact {
			err = w.runArtifactDeploy(ctx, app, d)
		} else {
			err = w.runGitDeploy(ctx, app, d)
		}
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

// runManualDeploy is the worker side of the dashboard "Deploy" button: the
// handler persisted image/port and queued the row. There is no repo, so no
// clone or build — the persisted image is pulled (bounded, so a hung
// registry can't hold the worker forever) and the shared zero-downtime swap
// runs. A manual deploy waits its turn behind any queued git build; deploys
// serialize by construction.
func (w *Worker) runManualDeploy(ctx context.Context, app store.App, d store.Deployment) error {
	image := d.ImageTag
	if image == "" {
		return errors.New("no image to deploy — submit the image field on the app page")
	}
	has, err := w.rt.HasImage(ctx, image)
	if err != nil {
		return fmt.Errorf("check image %s: %w", image, err)
	}
	if !has {
		w.log(d, "system", "pulling "+image)
		w.publish("deploy:"+app.Slug, "log", "pulling "+image)
		pctx, cancel := context.WithTimeout(ctx, w.pullTimeout)
		defer cancel()
		if err := w.rt.PullImage(pctx, image); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return fmt.Errorf("pull %s timed out after %s — check the image name and registry connectivity", image, w.pullTimeout)
			}
			return fmt.Errorf("pull %s: %w", image, err)
		}
		w.log(d, "system", "pulled "+image)
	}
	if err := w.runContainer(ctx, app, d, image, nil); err != nil {
		return err
	}
	return w.finish(d, app.ID)
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
	// Remember the framework (React, Spring, …) for the app's card. A best
	// guess from the manifests; nothing from the repo is executed.
	w.recordStack(app, stack.DetectDir(filepath.Join(checkoutDir, filepath.Clean("/"+app.RootDirectory))))
	// Webhook deploys skip the review page — leave the same range record in
	// the build log so every deploy shows what shipped.
	w.logDiffRecord(ctx, app, gs, d)
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

	// Webhook deploys skip the review page; leave the range record for them.
	w.logDiffRecord(ctx, app, gs, d)

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
	// Record the image's on-disk size for the /stats storage panel. The
	// inspect is best-effort — a daemon hiccup must never fail a deploy.
	size, err := w.rt.ImageSize(ctx, imageTag)
	if err != nil {
		slog.Warn("worker: image size inspect", "tag", imageTag, "err", err)
	}
	if _, err := w.store.CreateImage(store.Image{AppID: app.ID, Tag: imageTag, DeploymentID: d.ID, SizeBytes: int64(size)}); err != nil {
		return fmt.Errorf("record image: %w", err)
	}

	if err := w.runContainer(ctx, app, d, imageTag, map[string]string{"GIT_SHA": sha}); err != nil {
		return err
	}
	return w.finish(d, app.ID)
}

// logDiffRecord appends the deploy range to the build log for webhook
// deploys, which skip the review page — they get the same "what shipped"
// record the dashboard review shows. Best-effort: every error is swallowed,
// the deploy never fails because the record failed.
func (w *Worker) logDiffRecord(ctx context.Context, app store.App, gs store.GitSource, d store.Deployment) {
	if d.CommitSHA == "" || app.CurrentDeploymentID == "" {
		return
	}
	cur, err := w.store.GetDeployment(app.CurrentDeploymentID)
	if err != nil || cur.CommitSHA == "" {
		return
	}
	privateKey, err := crypto.Decrypt(w.encKey, gs.PrivateKeyEnc)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dir := gitpkg.MirrorDir(w.dataDir, gs.ID)
	if err := gitpkg.MirrorSync(ctx, gs.RepoURL, gs.DefaultBranch, privateKey, dir); err != nil {
		return
	}
	if err := gitpkg.MirrorEnsureSHA(ctx, dir, gs.RepoURL, privateKey, cur.CommitSHA); err != nil {
		return
	}
	rg, err := gitpkg.MirrorRange(ctx, dir, gs.DefaultBranch, cur.CommitSHA)
	if err != nil || len(rg.Commits) == 0 {
		return
	}
	w.log(d, "system", rg.SummaryLine())
	for _, c := range rg.Commits {
		w.log(d, "system", fmt.Sprintf("  %s %s — %s", c.Short, c.Subject, c.Author))
	}
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
// Both roll every replica (a resize is a rollout — limits are per container).
func (w *Worker) runRollback(ctx context.Context, app store.App, d store.Deployment) error {
	if d.ImageTag == "" {
		return errors.New("target has no image tag")
	}
	verb := "rolling back to"
	switch d.Kind {
	case "resize":
		verb = "applying resized limits, image"
	case "redeploy":
		verb = "redeploying with the current settings, image"
	}
	w.log(d, "system", verb+" "+d.ImageTag)
	w.publish("deploy:"+app.Slug, "log", verb+" "+d.ImageTag)
	if err := w.runContainer(ctx, app, d, d.ImageTag, nil); err != nil {
		return err
	}
	return w.finish(d, app.ID)
}

// runContainer rolls the app's replicas onto a new image, slot by slot
// (docs/specs/app-replicas.md). Each slot gets the same zero-downtime swap a
// single-container app always had: start the next container BESIDE the
// running one (a staged name + its own loopback host port + its own Traefik
// router), probe it, record its port, remove the old container, rename the
// staged one into the slot. A slot is never removed before its replacement
// is healthy, so on N >= 2 at least N-1 replicas serve throughout; N = 1 is
// exactly the pre-replicas swap.
//
// A failed slot halts the rollout: slots already replaced keep the new image,
// the rest keep the old one (drift is visible per slot on the app page), and
// the app keeps serving. Slots above the desired count are removed at the
// end, so a deploy converges the replica set.
func (w *Worker) runContainer(ctx context.Context, app store.App, d store.Deployment, imageTag string, extraEnv map[string]string) error {
	if err := w.rt.EnsureNetwork(ctx, w.network); err != nil {
		return err
	}
	env := w.buildEnv(app)
	port := app.Port
	if port == 0 {
		port = 8080 // platform convention; railpack apps read $PORT
		_ = w.store.UpdateAppPort(app.ID, port)
	}
	replicas := store.ClampReplicas(app.Replicas)
	domains := w.appDomains(app)
	// One priority for the whole deploy: every slot's router points at the
	// same service, and this deploy's routers outrank the previous deploy's.
	priority := time.Now().UnixNano()

	for slot := 1; slot <= replicas; slot++ {
		if replicas > 1 {
			msg := fmt.Sprintf("replica %d/%d: rolling onto %s", slot, replicas, imageTag)
			w.log(d, "system", msg)
			w.publish("deploy:"+app.Slug, "log", msg)
		}
		err := w.swapSlot(ctx, app, slot, slotOptions{
			image: imageTag, port: port, env: env, extraEnv: extraEnv,
			deployID: d.ID, routerDeployID: d.ID, domains: domains, priority: priority,
		})
		if err != nil {
			if errors.Is(err, swap.ErrStagedFailed) {
				msg := "new container failed its health probe — the previous container is still serving"
				if replicas > 1 {
					msg = fmt.Sprintf("replica %d/%d failed its health probe — rollout halted; replicas before it run the new image, the rest keep serving the previous one", slot, replicas)
				}
				w.log(d, "system", msg)
				w.publish("deploy:"+app.Slug, "log", msg)
			}
			return err
		}
	}
	w.removeSlotsAbove(ctx, app, replicas)
	return nil
}

// slotOptions is what differs between swapping a slot for a deploy and
// adding one for a scale-up.
type slotOptions struct {
	image          string
	port           int
	env            []string
	extraEnv       map[string]string
	deployID       string // deploymate.deploy label + replica row: the deployment whose image runs
	routerDeployID string // makes the router name unique (the deployment row doing the work)
	domains        []string
	priority       int64
}

// swapSlot starts a staged container for one slot and swaps it in
// (swap.Swap: probe → record port → remove old → rename). The replica row is
// written in OnFlip, before the old container goes away, so the preview
// proxy never resolves a slot to a refused port.
func (w *Worker) swapSlot(ctx context.Context, app store.App, slot int, o slotOptions) error {
	// The staged container needs its OWN host port while the old one still
	// publishes the slot's binding.
	hostPort := 0
	if o.port > 0 {
		var err error
		if hostPort, err = swap.ReserveLoopbackPort(); err != nil {
			return err
		}
	}
	spec := appspec.BuildSpec(app, appspec.Options{
		Image:      o.image,
		Name:       appspec.StagedSlotName(app.Slug, slot, o.routerDeployID),
		Port:       o.port,
		HostPort:   hostPort,
		Env:        o.env,
		ExtraEnv:   o.extraEnv,
		DeployID:   o.deployID,
		Network:    w.network,
		Domains:    o.domains,
		RouterName: appspec.RouterName(app.Slug, slot, o.routerDeployID),
		Priority:   o.priority,
		LEResolver: proxy.ResolverForLEMode(w.leMode),
		// Manual deploys/rollbacks/resizes inherit the stored overrides from
		// the row; git apps always have empty columns (nil = image default).
		Entrypoint: appspec.SplitArgs(app.Entrypoint),
		Cmd:        appspec.SplitArgs(app.Command),
		Slot:       slot,
	})
	slog.Info("worker: container spec", "app", app.Slug, "slot", slot, "mem_limit_mb", app.MemLimitMB, "cpu", app.CPULimit)
	w.publish("deploy:"+app.Slug, "deploy", "starting staged container "+spec.Name)
	if _, err := w.rt.Create(ctx, spec); err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	slotName := appspec.SlotName(app.Slug, slot)
	return swap.Swap(ctx, w.rt, slotName, spec, swap.Options{
		OnFlip: func(hostPort int) error {
			if slot == 1 {
				// Dual-write for one release: a binary rollback resolves
				// previews through apps.preview_host_port.
				if err := w.store.UpdateAppPreviewPort(app.ID, hostPort); err != nil {
					return err
				}
			}
			return w.store.UpsertAppReplica(store.AppReplica{
				AppID: app.ID, Slot: slot, ContainerName: slotName, HostPort: hostPort, DeployID: o.deployID,
			})
		},
		ProbeURL:      w.probeURL,
		ProbeAttempts: w.probeAttempts,
		ProbeInterval: w.probeInterval,
		// The failed container is removed right after; keep what it said.
		OnProbeFailed: func(summary string, lines []string) {
			emit := func(line string) {
				if err := w.store.AppendBuildLog(o.routerDeployID, "system", line); err != nil {
					slog.Error("worker: append build log", "err", err)
				}
				w.publish("deploy:"+app.Slug, "log", line)
			}
			if summary != "" {
				emit("the new container failed its health probe: " + summary)
			}
			if len(lines) == 0 {
				emit("(the container printed nothing)")
				return
			}
			emit("── the new container's last output ──")
			for _, l := range lines {
				emit(l)
			}
			emit("── end of container output ──")
		},
	})
}

// removeSlotsAbove drops the replica rows for slots above keep FIRST — the
// preview load balancer stops routing new requests to them — then gives
// each container a graceful stop (SIGTERM + the drain window, so in-flight
// requests finish) and removes it. Best-effort per container: a container
// that is already gone is the desired state.
func (w *Worker) removeSlotsAbove(ctx context.Context, app store.App, keep int) {
	if err := w.store.DeleteAppReplicasAbove(app.ID, keep); err != nil {
		slog.Error("worker: delete replica rows", "app", app.Slug, "err", err)
	}
	for slot := keep + 1; slot <= store.MaxReplicas; slot++ {
		name := appspec.SlotName(app.Slug, slot)
		_ = w.rt.Stop(ctx, name, swap.DefaultDrainTimeoutSec)
		if err := w.rt.Remove(ctx, name); err != nil && !errors.Is(err, runtime.ErrContainerNotFound) {
			slog.Warn("worker: remove extra replica", "app", app.Slug, "container", name, "err", err)
		}
	}
}

// runScale converges the running replica set to apps.replicas WITHOUT a
// build or a new image: missing slots start from the current deployment's
// image (d.ImageTag, copied from it at queue time) with the app row's
// current env/limits; slots above the count are stopped and removed. Slot 1
// is only touched to relabel a routed pre-replicas container (see below). Documented skew: if the app's env changed since the
// current deployment, new slots boot with the newer env — a redeploy
// converges.
func (w *Worker) runScale(ctx context.Context, app store.App, d store.Deployment) error {
	target := store.ClampReplicas(app.Replicas)
	if d.ImageTag == "" {
		return errors.New("no current image to scale — deploy the app first")
	}
	if err := w.rt.EnsureNetwork(ctx, w.network); err != nil {
		return err
	}
	rows, err := w.store.ListAppReplicas(app.ID)
	if err != nil {
		return fmt.Errorf("list replicas: %w", err)
	}
	// An app deployed before replicas has no rows; record its canonical
	// container as slot 1 so the preview proxy and monitor see the whole set.
	if len(rows) == 0 {
		s1 := appspec.Slots(app, nil)[0]
		if err := w.store.UpsertAppReplica(store.AppReplica{
			AppID: app.ID, Slot: 1, ContainerName: s1.Name, HostPort: s1.HostPort, DeployID: app.CurrentDeploymentID,
		}); err != nil {
			return fmt.Errorf("record slot 1: %w", err)
		}
	}
	env := w.buildEnv(app)
	domains := w.appDomains(app)
	port := app.Port
	if port == 0 {
		port = 8080
	}
	var extraEnv map[string]string
	if app.CurrentDeploymentID != "" {
		if cur, err := w.store.GetDeployment(app.CurrentDeploymentID); err == nil && cur.CommitSHA != "" {
			extraEnv = map[string]string{"GIT_SHA": cur.CommitSHA}
		}
	}

	// A routed slot 1 created before replicas carries the old Traefik label
	// shape (its service is named after its own router), so new slots could
	// never join its service. Relabel it first with the usual zero-downtime
	// swap — same image, current labels — so every slot shares one service.
	if len(domains) > 0 {
		if info, err := w.rt.Inspect(ctx, appspec.SlotName(app.Slug, 1)); err == nil && info.Running && info.Labels["deploymate.slot"] == "" {
			msg := "replica 1 predates replicas (old Traefik labels) — relabelling it with a zero-downtime swap"
			w.log(d, "system", msg)
			w.publish("deploy:"+app.Slug, "log", msg)
			if err := w.swapSlot(ctx, app, 1, slotOptions{
				image: d.ImageTag, port: port, env: env, extraEnv: extraEnv,
				deployID: app.CurrentDeploymentID, routerDeployID: d.ID, domains: domains,
				priority: time.Now().UnixNano(),
			}); err != nil {
				return fmt.Errorf("relabel replica 1: %w", err)
			}
		}
	}

	have := map[int]bool{}
	for _, r := range rows {
		if info, err := w.rt.Inspect(ctx, r.ContainerName); err == nil && info.Running {
			have[r.Slot] = true
		}
	}

	added := 0
	for slot := 2; slot <= target; slot++ {
		if have[slot] {
			continue
		}
		msg := fmt.Sprintf("replica %d/%d: starting from %s", slot, target, d.ImageTag)
		w.log(d, "system", msg)
		w.publish("deploy:"+app.Slug, "log", msg)
		// Priority 0: a scale slot joins the current deploy's shared service
		// and never outranks its routers.
		if err := w.swapSlot(ctx, app, slot, slotOptions{
			image: d.ImageTag, port: port, env: env, extraEnv: extraEnv,
			deployID: app.CurrentDeploymentID, routerDeployID: d.ID, domains: domains,
		}); err != nil {
			if errors.Is(err, swap.ErrStagedFailed) {
				w.log(d, "system", fmt.Sprintf("replica %d failed its health probe — removed; the running replicas are untouched", slot))
			}
			return err
		}
		added++
	}
	removed := 0
	for _, r := range rows {
		if r.Slot > target {
			removed++
		}
	}
	w.removeSlotsAbove(ctx, app, target)
	w.log(d, "system", fmt.Sprintf("scaled to %d replica(s): %d started, %d removed", target, added, removed))

	d.Status = "running"
	d.Error = ""
	d.FinishedAt = store.Now()
	// A scale is not a release: current_deployment_id keeps pointing at the
	// deployment whose image the replicas run.
	return w.store.UpdateDeployment(d)
}

// appDomains returns the app's routed hostnames for the Traefik labels.
func (w *Worker) appDomains(app store.App) []string {
	domains, err := w.store.ListDomains(app.ID)
	if err != nil {
		slog.Error("worker: list domains", "app", app.Slug, "err", err)
		return nil
	}
	hostnames := make([]string, 0, len(domains))
	for _, dm := range domains {
		hostnames = append(hostnames, dm.Hostname)
	}
	return hostnames
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
	// Fresh container with its binding — declare healthy now, matching the
	// dashboard deploy handler; the monitor corrects within 90s if the new
	// build actually fails to serve.
	if err := w.store.UpdateAppHealth(appID, "healthy"); err != nil {
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
		// With zero-downtime swaps the old containers usually keep serving
		// after a failed deploy — only mark the APP failed when no replica is
		// running for it.
		if !w.anySlotRunning(app) {
			_ = w.store.UpdateAppStatus(app.ID, "failed")
		}
	}
	slog.Error("worker: deployment failed", "deployment", d.ID, "err", err)
}

// anySlotRunning reports whether at least one of the app's replicas runs.
func (w *Worker) anySlotRunning(app store.App) bool {
	rows, _ := w.store.ListAppReplicas(app.ID)
	for _, sl := range appspec.Slots(app, rows) {
		if info, err := w.rt.Inspect(context.Background(), sl.Name); err == nil && info.Running {
			return true
		}
	}
	return false
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


// recordStack saves a detected framework on the app (for its card logo). An
// empty detection never clears a known one: an inconclusive scan is not
// evidence the app changed.
func (w *Worker) recordStack(app store.App, detected string) {
	if detected == "" || detected == app.Stack {
		return
	}
	if err := w.store.UpdateAppStack(app.ID, detected); err != nil {
		slog.Warn("worker: record stack", "app", app.Slug, "err", err)
	}
}
