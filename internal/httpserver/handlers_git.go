package httpserver

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/githubci"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
	"github.com/habibmuhammad/deploymate/web/templates"
)

func hexEncode(b []byte) string { return hex.EncodeToString(b) }

// handleGitConnect generates a deploy key + webhook secret and links the app
// to a new git source. The user then adds the public key to their forge and
// points the webhook at /hooks/{id}.
func (s *Server) handleGitConnect(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	_, _, refusal, err := s.connectRepoCore(app, r.FormValue("repo_url"), r.FormValue("provider"), r.FormValue("branch"))
	if err != nil {
		slog.Error("git: connect", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	// The deploy key must be on the repo before a private clone can work, so
	// the first deploy is a guided click on the app page, not an automatic one.
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Repository connected. Add the deploy key if it is private, then deploy."), http.StatusSeeOther)
}

// handleGitDeploy queues a deployment of the branch HEAD. The optional sha
// form field pins the deploy to a reviewed commit (the deploy-review page
// posts it); an empty sha keeps the old "deploy latest" behavior. The worker
// honors the pinned SHA (gitpkg.Clone fetches + detaches it).
func (s *Server) handleGitDeploy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if app.GitSourceID == "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Connect a repo first."), http.StatusSeeOther)
		return
	}
	// A prebuilt app has nothing to clone: the review page's deploy button
	// deploys the latest successful CI run instead.
	if app.DeployMode == store.DeployModeArtifact {
		s.handleDeployLatest(w, r)
		return
	}
	d, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "dashboard",
		CommitSHA: r.FormValue("sha"),
	})
	if err != nil {
		slog.Error("git: queue deploy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/deployments/"+d.ID, http.StatusSeeOther)
}

// handleDeployPreview shows what a deploy would ship — commits + file change
// counts vs the currently deployed commit — and is the confirm step of the
// two-step deploy flow. The confirm form pins the reviewed SHA.
func (s *Server) handleDeployPreview(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		slog.Error("deploy-preview: get project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if app.GitSourceID == "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Connect a repo first."), http.StatusSeeOther)
		return
	}
	gs, err := s.store.GetGitSource(app.GitSourceID)
	if err != nil {
		slog.Error("deploy-preview: get source", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	gitAuth, err := s.gitAuth().For(ctx, gs)
	if err != nil {
		slog.Error("deploy-preview: credentials", "err", err)
		http.Error(w, "could not reach the repository: "+err.Error(), http.StatusBadGateway)
		return
	}
	dir := gitpkg.MirrorDir(s.dataDir, gs.ID)
	if err := gitpkg.MirrorSyncAuth(ctx, gs.RepoURL, gs.DefaultBranch, gitAuth, dir); err != nil {
		slog.Error("deploy-preview: mirror sync", "err", err)
		http.Error(w, "could not reach the repository: "+err.Error(), http.StatusBadGateway)
		return
	}

	// The currently deployed commit ("" when nothing has deployed yet).
	deployedSHA := ""
	if app.CurrentDeploymentID != "" {
		if cur, err := s.store.GetDeployment(app.CurrentDeploymentID); err == nil {
			deployedSHA = cur.CommitSHA
		}
	}
	if err := gitpkg.MirrorEnsureSHAAuth(ctx, dir, gs.RepoURL, gitAuth, deployedSHA); err != nil {
		render(w, r, http.StatusOK, templates.DeployPreviewError(s.viewCtx(r), project, app,
			"the currently deployed commit ("+shortSHA(deployedSHA)+") is not on the remote anymore — likely force-pushed away. Roll back, or deploy HEAD without a diff."))
		return
	}
	rg, err := gitpkg.MirrorRange(ctx, dir, gs.DefaultBranch, deployedSHA)
	if err != nil {
		slog.Error("deploy-preview: range", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	render(w, r, http.StatusOK, templates.DeployPreviewPage(s.viewCtx(r), project, app, templates.DeployPreview{
		Range:       rg,
		DeployedSHA: deployedSHA,
		CommitURL:   gitpkg.CommitURL(gs.RepoURL, rg.Head),
		FirstDeploy: deployedSHA == "",
		NothingToDo: deployedSHA != "" && deployedSHA == rg.Head,
		Prebuilt:    app.DeployMode == store.DeployModeArtifact,
	}))
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// handleWebhook is the public push endpoint: /hooks/{sourceID}. No auth —
// the webhook secret is the credential. Responds fast so providers don't
// retry; the worker does the real work.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	sourceID := chi.URLParam(r, "id")
	gs, err := s.store.GetGitSource(sourceID)
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		slog.Error("webhook: lookup source", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	secret, err := crypto.Decrypt(s.encKey, gs.WebhookSecretEnc)
	if err != nil {
		slog.Error("webhook: decrypt secret", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var push webhooks.Push
	delivery := r.Header.Get("X-GitHub-Delivery")
	switch gs.Provider {
	case "github":
		if !webhooks.VerifyGitHub(secret, r.Header.Get("X-Hub-Signature-256"), body) {
			http.Error(w, "bad signature", http.StatusUnauthorized)
			return
		}
		// Dispatch on the event type (after authentication, so ping is not
		// an unauthenticated oracle). A missing header is treated as a push:
		// manual replays of a signed push (the multi-env workaround) never
		// carried one.
		switch event := r.Header.Get("X-GitHub-Event"); event {
		case "ping":
			_, _ = w.Write([]byte("pong"))
			return
		case "push", "":
		case "workflow_run":
			// Prebuilt deploys: a finished GitHub Actions run (see
			// handlers_ci.go for the gates).
			s.handleWorkflowRun(w, gs, body, delivery)
			return
		default:
			// Any other event (workflow_run, issues, …) is not a deploy.
			slog.Debug("webhook: ignoring non-push event", "source", gs.ID, "event", event)
			_, _ = w.Write([]byte("ignored: not a push event"))
			return
		}
		push, err = webhooks.ParseGitHubPush(body)
	case "gitlab":
		if !webhooks.VerifyGitLab(secret, r.Header.Get("X-GitLab-Token")) {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		delivery = r.Header.Get("X-Gitlab-Event-UUID")
		push, err = webhooks.ParseGitLabPush(body)
	default:
		http.Error(w, "unsupported provider", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "unparseable payload", http.StatusBadRequest)
		return
	}
	if push.Deleted {
		_, _ = w.Write([]byte("ignored: branch deleted"))
		return
	}
	if webhooks.BranchFromRef(push.Ref) != gs.DefaultBranch {
		_, _ = w.Write([]byte("ignored: not the deploy branch"))
		return
	}
	if s.deliveries.Seen(gs.Provider, gs.ID, delivery) {
		_, _ = w.Write([]byte("duplicate delivery ignored"))
		return
	}

	out := s.queuePushDeploys(gs, push)
	if out.queued == 0 && out.ciOnly > 0 {
		_, _ = w.Write([]byte("ignored: this app deploys from CI runs, not pushes"))
		return
	}
	if out.queued == 0 && out.skipped > 0 {
		_, _ = w.Write([]byte("ignored: no changes in the build folder"))
		return
	}
	if out.queued == 0 {
		slog.Warn("webhook: no apps linked to source", "source", gs.ID)
	}
	_, _ = w.Write([]byte("queued"))
}

// pushOutcome counts what a push did to the apps linked to one git source.
type pushOutcome struct{ queued, ciOnly, skipped int }

// queuePushDeploys queues a deployment for each app linked to the source that a
// push should deploy: not prebuilt apps (they deploy when their CI run finishes)
// and not apps whose build folder the push did not touch. Shared by the
// per-repository webhook and the GitHub App's webhook.
func (s *Server) queuePushDeploys(gs store.GitSource, push webhooks.Push) pushOutcome {
	var out pushOutcome
	apps, err := s.store.ListAppsByGitSource(gs.ID)
	if err != nil {
		return out
	}
	for _, app := range apps {
		if app.DeployMode == store.DeployModeArtifact {
			out.ciOnly++
			continue
		}
		if !push.TouchesFolder(app.RootDirectory) {
			out.skipped++
			_ = s.store.RecordEvent(app.ID, store.EventDeploySkipped,
				"push "+shortSHA(push.CommitSHA)+" changed nothing in "+orRoot(app.RootDirectory))
			continue
		}
		_, err := s.store.CreateDeployment(store.Deployment{
			AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "webhook",
			CommitSHA: push.CommitSHA, CommitMessage: push.CommitMessage,
		})
		if err != nil {
			slog.Error("webhook: queue deployment", "err", err)
			continue
		}
		out.queued++
	}
	return out
}

// randomHex returns n random bytes as hex (for webhook secrets).
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := cryptoRand.Read(b); err != nil {
		slog.Error("git: crypto/rand failed", "err", err)
		return "dm-insecure"
	}
	return hexEncode(b)
}

// handleDeploymentPage renders the live deployment view with its build log.
func (s *Server) handleDeploymentPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDeployment(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		slog.Error("deployments: get", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	app, err := s.store.GetAppByID(d.AppID)
	if err != nil {
		slog.Error("deployments: get app", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	project, err := s.store.GetProjectByID(app.ProjectID)
	if err != nil {
		slog.Error("deployments: get project", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Commit link to the forge, when the app has a git source.
	commitURL := ""
	if app.GitSourceID != "" && d.CommitSHA != "" {
		if gs, err := s.store.GetGitSource(app.GitSourceID); err == nil {
			commitURL = gitpkg.CommitURL(gs.RepoURL, d.CommitSHA)
		}
	}
	retry := false
	if d.Status == "failed" {
		if ds, err := s.store.ListDeployments(app.ID, 1); err == nil && len(ds) == 1 && ds[0].ID == d.ID {
			retry = true // only the newest deployment can be retried
		}
	}
	render(w, r, http.StatusOK, templates.DeploymentPage(s.viewCtx(r), project, app, d, commitURL, retry))
}

// handleDeploymentStream replays stored build lines then streams live events
// for the deployment's app over SSE.
func (s *Server) handleDeploymentStream(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDeployment(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	app, err := s.store.GetAppByID(d.AppID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sse.SetHeaders(w)
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Replay history so a reloaded page still shows the full log.
	lines, err := s.store.ListBuildLogs(d.ID, 0)
	if err == nil {
		for _, line := range lines {
			_ = sse.WriteEvent(w, sse.Event{Name: "log", Data: line})
		}
	}

	// Live events: the worker publishes to the app's deploy topic.
	ch, cancel := s.events.Subscribe("deploy:" + app.Slug)
	defer cancel()
	go sse.Heartbeat(r.Context().Done(), w, flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			_ = sse.WriteEvent(w, ev)
		}
	}
}

// handleRollback queues a rollback to a previous deployment's image.
func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDeployment(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	app, err := s.store.GetAppByID(d.AppID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	id, refusal, err := s.rollbackCore(d, app)
	if err != nil {
		slog.Error("deployments: queue rollback", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		http.Redirect(w, r, "/deployments/"+d.ID+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/deployments/"+id, http.StatusSeeOther)
}

// rollbackCore queues a rollback to deployment d's saved image.
func (s *Server) rollbackCore(d store.Deployment, app store.App) (id, refusal string, err error) {
	if d.ImageTag == "" {
		return "", "That deployment has no image to roll back to.", nil
	}
	rb, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "rollback", Status: "queued", Trigger: "rollback",
		ImageTag: d.ImageTag, CommitSHA: d.CommitSHA, CommitMessage: d.CommitMessage,
	})
	if err != nil {
		return "", "", err
	}
	return rb.ID, "", nil
}

// handleRotateWebhookSecret replaces the repo's webhook secret with a fresh
// random one. The old secret stops verifying at once, so the flash tells the
// owner to paste the new one into the provider's webhook settings — until they
// do, pushes are rejected (not silently ignored).
func (s *Server) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if app.GitSourceID == "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Connect a repo first."), http.StatusSeeOther)
		return
	}
	enc, err := crypto.Encrypt(s.encKey, randomHex(24))
	if err != nil {
		slog.Error("git: encrypt webhook secret", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetGitSourceWebhookSecret(app.GitSourceID, enc); err != nil {
		slog.Error("git: rotate webhook secret", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Webhook secret rotated. Paste the new secret into the webhook's settings on your git provider — pushes are rejected until you do.")+"#settings", http.StatusSeeOther)
}

// deployCore starts a deploy of whatever the app is set up to deploy: the
// latest successful CI run (prebuilt), a build of its repository, or its image.
// It returns the queued deployment's id, or a refusal sentence.
func (s *Server) deployCore(ctx context.Context, app store.App) (id, refusal string, err error) {
	switch {
	case app.GitSourceID != "" && app.DeployMode == store.DeployModeArtifact:
		id, msg := s.deployLatestRun(ctx, app)
		if id == "" {
			return "", msg, nil
		}
		return id, "", nil
	case app.GitSourceID != "":
		d, err := s.store.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "dashboard"})
		if err != nil {
			return "", "", err
		}
		return d.ID, "", nil
	case app.Image != "":
		d, err := s.store.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "manual", Status: "queued", Trigger: "manual", ImageTag: app.Image})
		if err != nil {
			return "", "", err
		}
		return d.ID, "", nil
	}
	return "", "Nothing to deploy yet — connect a git repository or set an image first.", nil
}

// handleRetry queues a copy of a failed deployment — same commit, same CI run,
// same image — so a transient failure (a flaky build, a registry blip, a
// service that was down) is one click, not a re-entry of what was deployed.
// It refuses anything that could deploy the wrong thing: a deployment that did
// not fail, one that is not the app's newest (a newer deploy exists), a second
// click while one is already in flight, and a CI run whose artifact is gone.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDeployment(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	app, err := s.store.GetAppByID(d.AppID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	id, _, refusal, err := s.retryCore(r.Context(), d, app)
	if err != nil {
		slog.Error("deployments: queue retry", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		http.Redirect(w, r, "/deployments/"+d.ID+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/deployments/"+id, http.StatusSeeOther)
}

// retryCore queues a copy of failed deployment d. It returns the id to go to
// (the new deployment, or the one already in flight, with a note saying so) or
// a refusal sentence; err is only for storage failures. Shared by the dashboard
// button and the API.
func (s *Server) retryCore(ctx context.Context, d store.Deployment, app store.App) (id, note, refusal string, err error) {
	if d.Status != "failed" {
		return "", "", "Only a failed deployment can be retried.", nil
	}
	recent, err := s.store.ListDeployments(app.ID, 5)
	if err != nil || len(recent) == 0 {
		return "", "", "", errors.New("list deployments")
	}
	for _, o := range recent {
		if o.Status == "queued" || o.Status == "building" {
			return o.ID, "another deployment is already in flight", "", nil
		}
	}
	if recent[0].ID != d.ID {
		return "", "", "A newer deployment exists — deploy the latest instead of retrying this one.", nil
	}
	// A prebuilt run whose artifact has expired can never succeed.
	if d.CIRun != 0 && app.DeployMode == store.DeployModeArtifact && app.GitSourceID != "" {
		if gs, gerr := s.store.GetGitSource(app.GitSourceID); gerr == nil {
			if repo, ok := githubci.ParseRepoURL(gs.RepoURL); ok {
				if gh, ok := s.githubClient(gs); ok {
					cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
					arts, aerr := gh.ListRunArtifacts(cctx, repo, d.CIRun)
					cancel()
					if aerr == nil && !hasLiveArtifact(arts, app.ArtifactName) {
						return "", "", fmt.Sprintf("CI run #%d has no artifact any more (GitHub deletes them after the workflow's retention-days). Use Run workflow now on the app page to build a fresh one.", d.CIRunNumber), nil
					}
				}
			}
		}
	}
	nd, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: d.Kind, Status: "queued", Trigger: "dashboard",
		CommitSHA: d.CommitSHA, CommitMessage: d.CommitMessage, ImageTag: d.ImageTag,
		CIRun: d.CIRun, CIRunNumber: d.CIRunNumber,
	})
	if err != nil {
		return "", "", "", err
	}
	return nd.ID, "", "", nil
}

// handleRedeploy restarts the app on the version it is already running, so
// changed settings (environment variables, resource limits) take effect
// without a new build or a new CI run. It is a "redeploy" deployment: the
// worker reuses the current deployment's local image and runs the same
// zero-downtime swap, with the app's settings read fresh. Prebuilt apps need
// this most — their CI run can only be deployed once.
func (s *Server) handleRedeploy(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	id, refusal, err := s.redeployCore(app)
	if err != nil {
		slog.Error("deployments: queue redeploy", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(refusal), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/deployments/"+id, http.StatusSeeOther)
}

// redeployCore queues a redeploy of the app's current image (settings re-read).
func (s *Server) redeployCore(app store.App) (id, refusal string, err error) {
	if app.CurrentDeploymentID == "" {
		return "", "Nothing is deployed yet — deploy first.", nil
	}
	cur, gerr := s.store.GetDeployment(app.CurrentDeploymentID)
	if gerr != nil || cur.ImageTag == "" {
		return "", "The current version has no saved image to redeploy — deploy again.", nil
	}
	d, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "redeploy", Status: "queued", Trigger: "dashboard",
		ImageTag: cur.ImageTag, CommitSHA: cur.CommitSHA, CommitMessage: cur.CommitMessage,
	})
	if err != nil {
		return "", "", err
	}
	return d.ID, "", nil
}
