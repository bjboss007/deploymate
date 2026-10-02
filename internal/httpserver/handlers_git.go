package httpserver

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/crypto"
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
	repoURL := strings.TrimSpace(r.FormValue("repo_url"))
	provider := r.FormValue("provider")
	branch := strings.TrimSpace(r.FormValue("branch"))
	if branch == "" {
		branch = "main"
	}
	if repoURL == "" ||
		!(strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "ssh://") || strings.HasPrefix(repoURL, "https://")) {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Repo URL must be SSH (git@github.com:you/repo.git) or HTTPS (https://github.com/you/repo.git)"), http.StatusSeeOther)
		return
	}
	if provider != "github" && provider != "gitlab" && provider != "gitea" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Unknown provider."), http.StatusSeeOther)
		return
	}

	key, err := gitpkg.GenerateDeployKey()
	if err != nil {
		slog.Error("git: generate key", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	privEnc, err := crypto.Encrypt(s.encKey, key.PrivateKeyPEM)
	if err != nil {
		slog.Error("git: encrypt key", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	secretEnc, err := crypto.Encrypt(s.encKey, randomHex(24))
	if err != nil {
		slog.Error("git: encrypt webhook secret", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	gs, err := s.store.CreateGitSource(store.GitSource{
		Provider:         provider,
		RepoURL:          repoURL,
		CloneMethod:      "deploy_key",
		PrivateKeyEnc:    privEnc,
		WebhookSecretEnc: secretEnc,
		DefaultBranch:    branch,
	})
	if err != nil {
		slog.Error("git: create source", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.store.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		slog.Error("git: link source", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug, http.StatusSeeOther)
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
	privateKey, err := crypto.Decrypt(s.encKey, gs.PrivateKeyEnc)
	if err != nil {
		slog.Error("deploy-preview: decrypt key", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	dir := gitpkg.MirrorDir(s.dataDir, gs.ID)
	if err := gitpkg.MirrorSync(ctx, gs.RepoURL, gs.DefaultBranch, privateKey, dir); err != nil {
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
	if err := gitpkg.MirrorEnsureSHA(ctx, dir, gs.RepoURL, privateKey, deployedSHA); err != nil {
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
		Range:        rg,
		DeployedSHA:  deployedSHA,
		CommitURL:    gitpkg.CommitURL(gs.RepoURL, rg.Head),
		FirstDeploy:  deployedSHA == "",
		NothingToDo:  deployedSHA != "" && deployedSHA == rg.Head,
		Prebuilt:     app.DeployMode == store.DeployModeArtifact,
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
		http.NotFound(w, r)
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
	if webhooks.BranchFromRef(push.Ref) != gs.DefaultBranch {
		_, _ = w.Write([]byte("ignored: not the deploy branch"))
		return
	}
	if s.deliveries.Seen(gs.Provider, gs.ID, delivery) {
		_, _ = w.Write([]byte("duplicate delivery ignored"))
		return
	}

	// Queue a deployment for the app(s) linked to this source.
	n, ciOnly := 0, 0
	if apps, err := s.store.ListAppsByGitSource(gs.ID); err == nil {
		for _, app := range apps {
			if app.DeployMode == store.DeployModeArtifact {
				// A prebuilt app deploys when its CI run finishes, not on the
				// push: the artifact doesn't exist yet.
				ciOnly++
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
			n++
		}
	}
	if n == 0 && ciOnly > 0 {
		_, _ = w.Write([]byte("ignored: this app deploys from CI runs, not pushes"))
		return
	}
	if n == 0 {
		slog.Warn("webhook: no apps linked to source", "source", gs.ID)
	}
	_, _ = w.Write([]byte("queued"))
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
		http.NotFound(w, r)
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
	render(w, r, http.StatusOK, templates.DeploymentPage(s.viewCtx(r), project, app, d, commitURL))
}

// handleDeploymentStream replays stored build lines then streams live events
// for the deployment's app over SSE.
func (s *Server) handleDeploymentStream(w http.ResponseWriter, r *http.Request) {
	d, err := s.store.GetDeployment(chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if d.ImageTag == "" {
		http.Redirect(w, r, "/deployments/"+d.ID+"?flash="+flashURL("That deployment has no image to roll back to."), http.StatusSeeOther)
		return
	}
	app, err := s.store.GetAppByID(d.AppID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rb, err := s.store.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "rollback", Status: "queued", Trigger: "rollback",
		ImageTag: d.ImageTag, CommitSHA: d.CommitSHA, CommitMessage: d.CommitMessage,
	})
	if err != nil {
		slog.Error("deployments: queue rollback", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/deployments/"+rb.ID, http.StatusSeeOther)
}