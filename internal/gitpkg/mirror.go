package gitpkg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Persistent mirror clones per git source, so the dashboard can show what a
// deploy would ship — commits and file stats vs the currently deployed
// commit — before it happens. Unlike the worker's ephemeral per-deploy
// checkout (Clone), mirrors keep full history: log ranges need it.
//
// Mirrors are read-mostly and rebuilt on demand: a deleted mirror dir is
// simply re-cloned by the next sync. No locking — a one-maintainer race
// between two previews of the same source just fetches twice.

// ErrCommitGone means the deployed commit is unreachable from the remote —
// typically force-pushed away. The preview cannot diff against it.
var ErrCommitGone = errors.New("gitpkg: deployed commit not found upstream")

// CommitInfo is one commit in a review range.
type CommitInfo struct {
	Hash    string
	Short   string
	Subject string
	Author  string
	Date    string // YYYY-MM-DD
}

// Range describes what a deploy would ship: the commits and file change
// counts between the deployed commit and the branch head.
type Range struct {
	Head    string
	Commits []CommitInfo
	Files   int
	Added   int
	Deleted int
}

// MirrorDir returns the mirror checkout path for a git source.
func MirrorDir(dataDir, sourceID string) string {
	return filepath.Join(dataDir, "repos", "mirror-"+sourceID)
}

// MirrorSync ensures a full (non-shallow) mirror of repoURL exists at dir
// and is up to date: clone on first use, fetch --prune afterwards.
func MirrorSync(ctx context.Context, repoURL, branch, privateKeyPEM, dir string) error {
	return MirrorSyncAuth(ctx, repoURL, branch, Auth{KeyPEM: privateKeyPEM}, dir)
}

// MirrorSyncAuth is MirrorSync with an explicit credential.
func MirrorSyncAuth(ctx context.Context, repoURL, branch string, auth Auth, dir string) error {
	privateKeyPEM := auth.KeyPEM
	env := auth.env(repoURL)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("prepare mirror dir: %w", err)
	}
	sshCmd, cleanup, err := sshCommand(repoURL, privateKeyPEM)
	if err != nil {
		return err
	}
	defer cleanup()

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if out, err := runGit(ctx, sshCmd, env, "clone", "--branch", branch, repoURL, dir); err != nil {
			return fmt.Errorf("mirror clone: %w: %s", err, out)
		}
		return nil
	}
	if out, err := runGit(ctx, sshCmd, env, "-C", dir, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("mirror fetch: %w: %s", err, out)
	}
	return nil
}

// MirrorEnsureSHA makes sha available in the mirror, fetching it from the
// remote on demand. Returns ErrCommitGone when the remote no longer has it.
func MirrorEnsureSHA(ctx context.Context, dir, repoURL, privateKeyPEM, sha string) error {
	return MirrorEnsureSHAAuth(ctx, dir, repoURL, Auth{KeyPEM: privateKeyPEM}, sha)
}

// MirrorEnsureSHAAuth is MirrorEnsureSHA with an explicit credential.
func MirrorEnsureSHAAuth(ctx context.Context, dir, repoURL string, auth Auth, sha string) error {
	privateKeyPEM := auth.KeyPEM
	env := auth.env(repoURL)
	if sha == "" {
		return nil
	}
	if _, err := runGit(ctx, "", nil, "-C", dir, "cat-file", "-e", sha+"^{commit}"); err == nil {
		return nil // already present
	}
	sshCmd, cleanup, err := sshCommand(repoURL, privateKeyPEM)
	if err != nil {
		return err
	}
	defer cleanup()
	if out, err := runGit(ctx, sshCmd, env, "-C", dir, "fetch", "origin", sha); err != nil {
		return fmt.Errorf("%w (%s)", ErrCommitGone, strings.TrimSpace(out))
	}
	return nil
}

// MirrorRange diffs the deployed commit against origin/branch. An empty
// deployedSHA is a first deploy: the range reports just the head commit.
// Returns an empty Range when the deployed commit IS the branch head.
func MirrorRange(ctx context.Context, dir, branch, deployedSHA string) (Range, error) {
	var r Range
	head, err := runGitOut(ctx, "", nil, "-C", dir, "rev-parse", "origin/"+branch)
	if err != nil {
		return r, fmt.Errorf("resolve branch head: %w", err)
	}
	head = strings.TrimSpace(head)
	r.Head = head
	if head == "" {
		return r, fmt.Errorf("branch %q not found in mirror", branch)
	}

	if deployedSHA == "" {
		// First deploy: nothing to diff, show just the commit about to ship.
		commits, err := commitInfos(ctx, dir, head, true)
		if err != nil {
			return r, err
		}
		r.Commits = commits
		return r, nil
	}
	if deployedSHA == head {
		return r, nil // nothing to deploy
	}

	commits, err := commitInfos(ctx, dir, deployedSHA+".."+head, false)
	if err != nil {
		return r, err
	}
	r.Commits = commits

	out, err := runGitOut(ctx, "", nil, "-C", dir, "diff", "--numstat", deployedSHA+".."+head)
	if err != nil {
		return r, fmt.Errorf("diff stat: %w", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "-" {
			continue // binary files report "-" and aren't counted
		}
		added, errA := strconv.Atoi(fields[0])
		deleted, errD := strconv.Atoi(fields[1])
		if errA != nil || errD != nil {
			continue
		}
		r.Files++
		r.Added += added
		r.Deleted += deleted
	}
	return r, nil
}

// SummaryLine renders the range as the one-line record: "3 commits, 5 files,
// +12/-4".
func (r Range) SummaryLine() string {
	return fmt.Sprintf("%d commits, %d files, +%d/-%d", len(r.Commits), r.Files, r.Added, r.Deleted)
}

func commitInfos(ctx context.Context, dir, rev string, single bool) ([]CommitInfo, error) {
	args := []string{"log", "--format=%H%x1f%h%x1f%s%x1f%an%x1f%ad", "--date=short"}
	if single {
		args = append(args, "-1")
	}
	args = append(args, rev)
	argv := append([]string{"-C", dir}, args...)
	out, err := runGitOut(ctx, "", nil, argv...)
	if err != nil {
		return nil, fmt.Errorf("log %s: %w", rev, err)
	}
	var infos []CommitInfo
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 5 {
			continue
		}
		infos = append(infos, CommitInfo{Hash: parts[0], Short: parts[1], Subject: parts[2], Author: parts[3], Date: parts[4]})
	}
	return infos, nil
}

// sshCommand returns the GIT_SSH_COMMAND fragment (or "") for a repo URL,
// writing the deploy key to a temp file with a cleanup func. HTTPS URLs need
// no key.
func sshCommand(repoURL, privateKeyPEM string) (string, func(), error) {
	noop := func() {}
	if !strings.HasPrefix(repoURL, "git@") && !strings.HasPrefix(repoURL, "ssh://") {
		return "", noop, nil
	}
	dir, err := os.MkdirTemp("", "dm-ssh-")
	if err != nil {
		return "", noop, fmt.Errorf("temp dir for deploy key: %w", err)
	}
	keyPath := filepath.Join(dir, "id")
	if err := os.WriteFile(keyPath, []byte(privateKeyPEM), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", noop, fmt.Errorf("write deploy key: %w", err)
	}
	cmd := fmt.Sprintf(
		"ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes",
		keyPath,
	)
	return cmd, func() { os.RemoveAll(dir) }, nil
}
