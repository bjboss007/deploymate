package httpserver

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/githubci"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
)

// maxCommitMessage bounds the commit message stored from a CI payload.
const maxCommitMessage = 500

// ciSkipReason decides whether a completed, successful workflow run should
// deploy an app in artifact mode, returning "" to deploy or a short
// operator-readable reason to skip. These are the gates from
// docs/specs/prebuilt-deploys.md; the payload is already HMAC-verified.
//
//  1. (caller) action == completed and conclusion == success
//  2. the run is of THIS app's workflow file
//  3. the run is on the source's tracked branch
//  4. it was triggered by a push or a manual dispatch — never a
//     pull_request: a fork PR whose branch is named like the deploy branch
//     would otherwise get its (attacker-built) artifact deployed
//  5. the code came from the same repository (not a fork)
//  6. one deployment per run, and never one older than what is already
//     deployed or in flight (a late finisher or a "Re-run" of an old
//     workflow must not roll anything back)
func (s *Server) ciSkipReason(app store.App, gs store.GitSource, wr webhooks.WorkflowRun) string {
	if app.DeployMode != store.DeployModeArtifact {
		return "this app builds on the server"
	}
	if wr.Path != app.WorkflowPath {
		return "a different workflow"
	}
	if wr.HeadBranch != gs.DefaultBranch {
		return "not the deploy branch"
	}
	if wr.Event != "push" && wr.Event != "workflow_dispatch" {
		return "only push and manual runs deploy"
	}
	if !strings.EqualFold(wr.HeadRepo, wr.Repo) {
		return "the run is from a fork"
	}
	if seen, err := s.store.HasCIRun(app.ID, wr.RunID); err != nil {
		slog.Error("webhook: check ci run", "err", err)
		return "internal error"
	} else if seen {
		return "this run was already handled"
	}
	if latest, err := s.store.LatestCIRunNumber(app.ID); err != nil {
		slog.Error("webhook: latest ci run number", "err", err)
		return "internal error"
	} else if wr.RunNumber <= latest {
		return "a newer run is already deployed"
	}
	return ""
}

// handleWorkflowRun processes a (verified) GitHub workflow_run delivery for a
// source: every linked artifact-mode app whose gates pass gets a queued
// `ci` deployment for the run. Everything else is acknowledged and ignored
// with a short fixed reason.
func (s *Server) handleWorkflowRun(w http.ResponseWriter, gs store.GitSource, body []byte, delivery string) {
	wr, err := webhooks.ParseGitHubWorkflowRun(body)
	if err != nil {
		http.Error(w, "unparseable payload", http.StatusBadRequest)
		return
	}
	// A run produces requested → in_progress → completed deliveries; only a
	// successful completion can deploy.
	if wr.Action != "completed" {
		_, _ = w.Write([]byte("ignored: workflow run is not completed"))
		return
	}
	if wr.Conclusion != "success" {
		_, _ = w.Write([]byte("ignored: workflow run did not succeed"))
		return
	}
	// The hook belongs to one repository; a payload about another is wrong.
	if repo, ok := githubci.ParseRepoURL(gs.RepoURL); ok && !strings.EqualFold(repo, wr.Repo) {
		_, _ = w.Write([]byte("ignored: run belongs to a different repository"))
		return
	}
	if s.deliveries.Seen(gs.Provider, gs.ID, delivery) {
		_, _ = w.Write([]byte("duplicate delivery ignored"))
		return
	}

	apps, err := s.store.ListAppsByGitSource(gs.ID)
	if err != nil {
		slog.Error("webhook: list apps for source", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	msg := wr.HeadMessage
	if len(msg) > maxCommitMessage {
		msg = msg[:maxCommitMessage]
	}
	queued, firstReason := 0, ""
	for _, app := range apps {
		if app.DeployMode != store.DeployModeArtifact {
			continue // build-mode apps deploy from pushes, never from CI events
		}
		if reason := s.ciSkipReason(app, gs, wr); reason != "" {
			if firstReason == "" {
				firstReason = reason
			}
			continue
		}
		if _, err := s.store.CreateDeployment(store.Deployment{
			AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "ci",
			CommitSHA: wr.HeadSHA, CommitMessage: msg, CIRun: wr.RunID, CIRunNumber: wr.RunNumber,
		}); err != nil {
			slog.Error("webhook: queue ci deployment", "app", app.Slug, "err", err)
			continue
		}
		queued++
	}
	switch {
	case queued > 0:
		_, _ = w.Write([]byte("queued"))
	case firstReason != "":
		_, _ = w.Write([]byte("ignored: " + firstReason))
	default:
		_, _ = w.Write([]byte("ignored: no app on this source deploys from CI runs"))
	}
}
