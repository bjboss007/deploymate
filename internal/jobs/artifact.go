package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/builder"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/githubci"
	"github.com/habibmuhammad/deploymate/internal/stack"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// runArtifactDeploy deploys a JAR that GitHub Actions built
// (docs/specs/prebuilt-deploys.md): download the run's artifact with the
// source's token, take the one JAR out of it, wrap it in a Temurin JRE image
// (seconds, no compiler, no build cache), then run the usual rolling,
// replica-aware swap. Nothing is compiled on this server, so a 2 GB box can
// run what it could never build. Every failure names its cause and leaves
// the previous version serving (nothing changes until the swap).
func (w *Worker) runArtifactDeploy(ctx context.Context, app store.App, d store.Deployment) error {
	if d.CIRun == 0 {
		return errors.New("this app deploys from GitHub Actions runs, not from a clone — push to the tracked branch and let its workflow finish, or switch the app back to building on this server")
	}
	if app.GitSourceID == "" {
		return errors.New("no git source connected — connect the repo on the app page")
	}
	gs, err := w.store.GetGitSource(app.GitSourceID)
	if err != nil {
		return fmt.Errorf("git source: %w", err)
	}
	repo, ok := githubci.ParseRepoURL(gs.RepoURL)
	if !ok {
		return fmt.Errorf("cannot read owner/repo from the repository URL %q", gs.RepoURL)
	}
	if gs.APITokenEnc == "" {
		return errors.New("no GitHub token is set for this repository — add a fine-grained token (Actions: read) on the app page")
	}
	token, err := crypto.Decrypt(w.encKey, gs.APITokenEnc)
	if err != nil {
		return fmt.Errorf("decrypt GitHub token: %w", err)
	}
	gh := githubci.New(w.githubAPI, token)
	say := func(line string) {
		w.log(d, "system", line)
		w.publish("deploy:"+app.Slug, "log", line)
	}

	say(fmt.Sprintf("fetching the artifact of %s run %d (commit %s)", repo, d.CIRun, shortCommit(d.CommitSHA)))
	arts, err := gh.ListRunArtifacts(ctx, repo, d.CIRun)
	if err != nil {
		return describeGitHubError(err, repo)
	}
	art, err := pickArtifact(arts, app.ArtifactName, d.CIRun)
	if err != nil {
		return err
	}

	workDir := filepath.Join(w.dataDir, "builds", d.ID)
	defer func() { _ = os.RemoveAll(workDir) }()
	ctxDir := filepath.Join(workDir, "context")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return err
	}
	zipPath := filepath.Join(workDir, "artifact.zip")
	zf, err := os.OpenFile(zipPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, sum, dlErr := gh.DownloadArtifact(ctx, repo, art.ID, zf, builder.MaxArtifactZipBytes)
	_ = zf.Close()
	if dlErr != nil {
		if errors.Is(dlErr, githubci.ErrTooLarge) {
			return fmt.Errorf("the artifact is over the %d MB limit", builder.MaxArtifactZipBytes>>20)
		}
		return describeGitHubError(dlErr, repo)
	}
	say(fmt.Sprintf("downloaded %q (%d KB, sha256 %s)", art.Name, n>>10, sum[:12]))
	// GitHub's digest is the sha256 of the zip; when it gave one, hold it to it.
	if art.Digest != "" && !strings.EqualFold(art.Digest, "sha256:"+sum) {
		return errors.New("the artifact failed its integrity check (the download does not match GitHub's digest) — re-run the workflow")
	}

	jarName, jarSize, err := builder.ExtractJar(zipPath, filepath.Join(ctxDir, "app.jar"), builder.MaxJarBytes)
	if err != nil {
		return err
	}
	w.recordStack(app, stack.DetectJar(filepath.Join(ctxDir, "app.jar"))) // Spring Boot fat jars show the Spring logo
	major := builder.JavaMajor(app.Runtime)
	say(fmt.Sprintf("wrapping %s (%d KB) in eclipse-temurin:%s-jre", jarName, jarSize>>10, major))
	dockerfile, err := builder.JavaWrapperDockerfile(major)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return err
	}

	imageTag := fmt.Sprintf("deploymate/apps/%s:%s", app.Slug, d.ID)
	build := w.buildFn
	if build == nil {
		build = builder.Build
	}
	if err := build(ctx, ctxDir, "", imageTag, func(line string) {
		w.log(d, "stdout", line)
		w.publish("deploy:"+app.Slug, "log", line)
	}); err != nil {
		return err
	}

	d.ImageTag = imageTag
	_ = w.store.UpdateDeployment(d)
	size, err := w.rt.ImageSize(ctx, imageTag)
	if err != nil {
		slog.Warn("worker: image size inspect", "tag", imageTag, "err", err)
	}
	if _, err := w.store.CreateImage(store.Image{AppID: app.ID, Tag: imageTag, DeploymentID: d.ID, SizeBytes: int64(size)}); err != nil {
		return fmt.Errorf("record image: %w", err)
	}

	if err := w.runContainer(ctx, app, d, imageTag, map[string]string{"GIT_SHA": d.CommitSHA}); err != nil {
		return err
	}
	return w.finish(d, app.ID)
}

// pickArtifact finds the app's artifact in a run's list, with a message per
// way it can be missing.
func pickArtifact(arts []githubci.Artifact, name string, runID int64) (githubci.Artifact, error) {
	var names []string
	for _, a := range arts {
		names = append(names, a.Name)
		if a.Name != name {
			continue
		}
		if a.Expired {
			return a, fmt.Errorf("the artifact %q of run %d has expired — re-run the workflow (artifacts are kept for the workflow's retention-days)", name, runID)
		}
		if a.SizeBytes > builder.MaxArtifactZipBytes {
			return a, fmt.Errorf("the artifact is %d MB, over the %d MB limit", a.SizeBytes>>20, builder.MaxArtifactZipBytes>>20)
		}
		return a, nil
	}
	if len(names) == 0 {
		// GitHub deletes a run's artifacts when their retention ends (1 day
		// in the generated workflow), after which the run lists none.
		return githubci.Artifact{}, fmt.Errorf("workflow run %d has no artifacts any more — they expire after the workflow's retention-days (1 day in the generated workflow), or the run never uploaded one. Re-run the workflow on GitHub and deploy the new run", runID)
	}
	return githubci.Artifact{}, fmt.Errorf("workflow run %d succeeded but uploaded no artifact named %q (it has: %s) — check the upload-artifact step's name", runID, name, strings.Join(names, ", "))
}

// describeGitHubError turns a GitHub API failure into the message the
// operator needs to fix it.
func describeGitHubError(err error, repo string) error {
	var ae *githubci.APIError
	if !errors.As(err, &ae) {
		return fmt.Errorf("could not reach GitHub: %w", err)
	}
	switch ae.Status {
	case 401:
		return fmt.Errorf("the GitHub token was rejected for %s (HTTP 401: %s) — create a fine-grained token with Actions: read on this repository and replace it on the app page", repo, ae.Message)
	case 403:
		return fmt.Errorf("GitHub refused the token for %s (HTTP 403: %s) — it needs the Actions: read permission on this repository (and organization approval, if your organization requires it)", repo, ae.Message)
	case 404:
		return fmt.Errorf("GitHub could not find %s or this run (HTTP 404) — a token without access to a private repository gets 404; check the token's repository access", repo)
	default:
		return fmt.Errorf("GitHub returned an error for %s: %w", repo, ae)
	}
}
