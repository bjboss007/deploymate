package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/githubci"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
)

// SetGitHubAPI points the prebuilt-deploy UI at a different GitHub API base
// URL. Tests only (the e2e's fake GitHub); empty keeps the real API.
func (s *Server) SetGitHubAPI(baseURL string) { s.githubAPI = baseURL }

var (
	workflowPathRe = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9._-]+\.ya?ml$`)
	artifactNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// maxTokenLen bounds a pasted GitHub token (fine-grained ones are ~100 chars).
const maxTokenLen = 255

// prebuiltSource loads the app's git source for a prebuilt-deploy action and
// redirects with a message when the app can't use prebuilt mode at all
// (no repo, or not GitHub). ok=false means the response is already written.
func (s *Server) prebuiltSource(w http.ResponseWriter, r *http.Request, app store.App) (store.GitSource, string, bool) {
	back := func(msg string) (store.GitSource, string, bool) {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
		return store.GitSource{}, "", false
	}
	if app.GitSourceID == "" {
		return back("Connect a repo first.")
	}
	gs, err := s.store.GetGitSource(app.GitSourceID)
	if err != nil {
		slog.Error("prebuilt: get source", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return gs, "", false
	}
	repo, ok := githubci.ParseRepoURL(gs.RepoURL)
	if gs.Provider != "github" || !ok {
		return back("Prebuilt deploys need a GitHub repository.")
	}
	return gs, repo, true
}

// githubClient builds a client from the source's stored token; "" when none.
func (s *Server) githubClient(gs store.GitSource) (*githubci.Client, bool) {
	if gs.APITokenEnc == "" {
		return nil, false
	}
	token, err := crypto.Decrypt(s.encKey, gs.APITokenEnc)
	if err != nil {
		slog.Error("prebuilt: decrypt token", "source", gs.ID, "err", err)
		return nil, false
	}
	return githubci.New(s.githubAPI, token), true
}

// ghFlash turns a GitHub API failure into a sentence the owner can act on.
// The token itself never appears in an error (githubci redacts), and GitHub's
// message text is not echoed.
func ghFlash(err error, repo string) string {
	var ae *githubci.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusUnauthorized:
			return "GitHub rejected the token — it is wrong or expired. Create a new fine-grained token and save it."
		case http.StatusForbidden:
			return "GitHub refused the request — the token needs Actions: read-only on " + repo + ", or the organization has not approved it yet."
		case http.StatusNotFound:
			return "GitHub says " + repo + " (or the workflow) was not found — or the token was not granted access to that repository."
		}
	}
	return "Could not reach GitHub: " + err.Error()
}

// handleDeployMode saves how the app deploys (build on this server, or
// prebuilt from GitHub Actions) plus, for prebuilt, the workflow, artifact
// name and — write-only — the API token. A blank token keeps the stored one.
func (s *Server) handleDeployMode(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	gs, _, ok := s.prebuiltSource(w, r, app)
	if !ok {
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
	}
	mode := r.FormValue("mode")
	if mode != store.DeployModeBuild && mode != store.DeployModeArtifact {
		back("Unknown deploy mode.")
		return
	}
	workflow := strings.TrimSpace(r.FormValue("workflow_path"))
	artifact := strings.TrimSpace(r.FormValue("artifact_name"))
	if workflow == "" {
		workflow = store.DefaultWorkflowPath
	}
	if artifact == "" {
		artifact = store.DefaultArtifactName
	}
	if !workflowPathRe.MatchString(workflow) {
		back("Workflow must be a file under .github/workflows/ ending in .yml or .yaml.")
		return
	}
	if !artifactNameRe.MatchString(artifact) {
		back("Artifact name may only use letters, digits, dot, dash and underscore (max 100).")
		return
	}

	token := strings.TrimSpace(r.FormValue("api_token"))
	if len(token) > maxTokenLen || strings.ContainsAny(token, " \t\r\n") {
		back("That does not look like a GitHub token.")
		return
	}
	hasToken := gs.APITokenEnc != ""
	switch {
	case token != "":
		enc, err := crypto.Encrypt(s.encKey, token)
		if err != nil {
			slog.Error("prebuilt: encrypt token", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if err := s.store.SetGitSourceAPIToken(gs.ID, enc); err != nil {
			slog.Error("prebuilt: save token", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		hasToken = true
	case r.FormValue("clear_token") == "on":
		if err := s.store.SetGitSourceAPIToken(gs.ID, ""); err != nil {
			slog.Error("prebuilt: clear token", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		hasToken = false
	}
	if mode == store.DeployModeArtifact && !hasToken {
		back("Prebuilt mode needs a GitHub token (fine-grained, Actions: read-only) — paste it below.")
		return
	}
	if err := s.store.UpdateAppDeployMode(app.ID, mode, workflow, artifact); err != nil {
		slog.Error("prebuilt: save mode", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if mode == store.DeployModeArtifact {
		back("Saved. Prebuilt mode: CI builds, DeployMate deploys. Test the connection, then add the workflow to the repo.")
		return
	}
	back("Saved. This app builds on this server again.")
}

// handleGitTest checks, without deploying anything, that the saved token
// reaches the repo and the workflow, and warns when the token is wider than
// this one repository (a fine-grained token created with "All repositories"
// can read every private repo's Actions artifacts — spike S1-B).
func (s *Server) handleGitTest(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	gs, repo, ok := s.prebuiltSource(w, r, app)
	if !ok {
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
	}
	gh, ok := s.githubClient(gs)
	if !ok {
		back("Save a GitHub token first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	info, err := gh.GetRepo(ctx, repo)
	if err != nil {
		back(ghFlash(err, repo))
		return
	}
	runs, err := gh.ListSuccessfulRuns(ctx, repo, app.WorkflowPath, gs.DefaultBranch, 5)
	if err != nil {
		var ae *githubci.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			back("Connected to " + info.FullName + ", but the workflow " + app.WorkflowPath + " was not found — add it to the repo first (copy it from this page).")
			return
		}
		back(ghFlash(err, repo))
		return
	}

	msg := fmt.Sprintf("Connected to %s. Workflow %s found; %d successful run(s) on %s.", info.FullName, app.WorkflowPath, len(runs), gs.DefaultBranch)
	if n, more, err := gh.OtherPrivateRepos(ctx, repo); err != nil {
		msg += " (Could not check how widely the token is scoped.)"
	} else if n > 0 {
		plus := ""
		if more {
			plus = "+"
		}
		msg += fmt.Sprintf(" ⚠ This token can also read Actions artifacts of %d%s other private repositories — recreate it with Only select repositories → %s.", n, plus, repo)
	} else {
		msg += " The token is scoped to this repository."
	}
	back(msg)
}

// deployLatestRun queues a deployment for the newest successful run that
// passes the same gates a webhook delivery does — so a manual deploy can
// never do what CI events cannot (deploy a fork's run, an older run, or the
// same run twice). It returns the new deployment's id, or "" and a message.
func (s *Server) deployLatestRun(ctx context.Context, app store.App) (id, msg string) {
	if app.GitSourceID == "" {
		return "", "Connect a repo first."
	}
	gs, err := s.store.GetGitSource(app.GitSourceID)
	if err != nil {
		slog.Error("prebuilt: get source", "err", err)
		return "", "Internal error."
	}
	repo, ok := githubci.ParseRepoURL(gs.RepoURL)
	if !ok || gs.Provider != "github" {
		return "", "Prebuilt deploys need a GitHub repository."
	}
	gh, ok := s.githubClient(gs)
	if !ok {
		return "", "Save a GitHub token first."
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runs, err := gh.ListSuccessfulRuns(ctx, repo, app.WorkflowPath, gs.DefaultBranch, 10)
	if err != nil {
		return "", ghFlash(err, repo)
	}
	if len(runs) == 0 {
		return "", "No successful run of " + app.WorkflowPath + " on " + gs.DefaultBranch + " yet — push, or run the workflow from GitHub's Actions tab, then try again."
	}
	reason := ""
	for _, run := range runs {
		wr := webhooks.WorkflowRun{
			Action: "completed", Conclusion: "success", RunID: run.ID, RunNumber: run.RunNumber,
			Path:  app.WorkflowPath, // the runs were listed by this workflow file
			Event: run.Event, HeadBranch: run.HeadBranch, HeadSHA: run.HeadSHA,
			HeadRepo: run.HeadRepository.FullName, Repo: repo,
		}
		if wr.HeadRepo == "" {
			wr.HeadRepo = repo
		}
		if r := s.ciSkipReason(app, gs, wr); r != "" {
			if reason == "" {
				reason = fmt.Sprintf("Latest successful run (#%d): %s.", run.RunNumber, r)
			}
			continue
		}
		// A run whose artifact has expired (retention-days) can never deploy:
		// skip it rather than queue a deployment that is bound to fail. A
		// failed lookup is not proof, so only a clear "gone" skips.
		if arts, err := gh.ListRunArtifacts(ctx, repo, run.ID); err == nil && !hasLiveArtifact(arts, app.ArtifactName) {
			if reason == "" {
				reason = fmt.Sprintf("The latest successful run (#%d) has no downloadable %q artifact any more — GitHub deletes them after the workflow's retention-days. Use Run workflow now to build a fresh one.", run.RunNumber, app.ArtifactName)
			}
			continue
		}
		msg := run.HeadCommit.Message
		if len(msg) > maxCommitMessage {
			msg = msg[:maxCommitMessage]
		}
		d, err := s.store.CreateDeployment(store.Deployment{
			AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "dashboard",
			CommitSHA: run.HeadSHA, CommitMessage: msg, CIRun: run.ID, CIRunNumber: run.RunNumber,
		})
		if err != nil {
			slog.Error("prebuilt: queue deployment", "err", err)
			return "", "Internal error."
		}
		return d.ID, ""
	}
	return "", reason
}

func hasLiveArtifact(arts []githubci.Artifact, name string) bool {
	for _, a := range arts {
		if a.Name == name && !a.Expired {
			return true
		}
	}
	return false
}

// handleRunWorkflow starts a fresh CI run (workflow_dispatch). The run's
// completion arrives as the usual workflow_run webhook and deploys through the
// same gates, so nothing is queued here.
func (s *Server) handleRunWorkflow(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
	}
	if app.DeployMode != store.DeployModeArtifact {
		back("This app builds on the server — switch it to prebuilt mode first.")
		return
	}
	gs, repo, ok := s.prebuiltSource(w, r, app)
	if !ok {
		return
	}
	gh, ok := s.githubClient(gs)
	if !ok {
		back("Save a GitHub token first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := gh.DispatchWorkflow(ctx, repo, app.WorkflowPath, gs.DefaultBranch); err != nil {
		var ae *githubci.APIError
		if errors.As(err, &ae) {
			switch ae.Status {
			case http.StatusForbidden, http.StatusNotFound:
				back("GitHub would not start the workflow. Starting runs needs the token's Actions permission set to read and write (it is read-only for deploys), and " + app.WorkflowPath + " must have the workflow_dispatch trigger on " + gs.DefaultBranch + ". You can also press Run workflow on GitHub's Actions tab.")
				return
			case http.StatusUnprocessableEntity:
				back(app.WorkflowPath + " has no workflow_dispatch trigger on " + gs.DefaultBranch + " — add it (the generated workflow has it) or run it from GitHub.")
				return
			}
		}
		back(ghFlash(err, repo))
		return
	}
	back("Started " + app.WorkflowPath + " on " + gs.DefaultBranch + ". DeployMate deploys it when it finishes (needs the Workflow runs webhook); or press Deploy latest run in a minute or two.")
}

// handleDeployLatest is the "Deploy latest successful run" button.
func (s *Server) handleDeployLatest(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	if app.DeployMode != store.DeployModeArtifact {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("This app builds on the server — switch it to prebuilt mode first."), http.StatusSeeOther)
		return
	}
	id, msg := s.deployLatestRun(r.Context(), app)
	if id == "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/deployments/"+id, http.StatusSeeOther)
}
