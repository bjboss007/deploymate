// Package gitpkg wraps the git CLI for clone and deploy-key operations.
// DeployMate shells out to git rather than linking libgit2: the CLI is
// ubiquitous, handles SSH natively, and partial clones keep checkouts small.
package gitpkg

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// DeployKey is a freshly generated ed25519 keypair. PublicKey is the
// "ssh-ed25519 AAAA..." line the user pastes into GitHub/GitLab/Gitea.
type DeployKey struct {
	PrivateKeyPEM string
	PublicKey     string
}

// GenerateDeployKey creates an ed25519 keypair for read access to a repo
// using the real ssh-keygen binary — the one tool guaranteed to produce keys
// every ssh client accepts.
func GenerateDeployKey() (*DeployKey, error) {
	dir, err := os.MkdirTemp("", "dm-key-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "id_ed25519")

	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "deploymate", "-q", "-f", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh-keygen: %w: %s", err, out)
	}
	priv, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	return &DeployKey{
		PrivateKeyPEM: string(priv),
		PublicKey:     strings.TrimSpace(string(pub)),
	}, nil
}

// Clone checks out repoURL into destDir. branch is required; commitSHA, when
// non-empty, pins the checkout to that commit. SSH URLs (git@…, ssh://…)
// authenticate with the deploy key written to a 0600 temp file; HTTPS URLs
// clone without a key (public repos, or PAT-in-URL later).
func Clone(ctx context.Context, repoURL, branch, commitSHA, privateKeyPEM, destDir string) error {
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return fmt.Errorf("prepare clone dir: %w", err)
	}

	sshCmd := ""
	if strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "ssh://") {
		keyPath := filepath.Join(destDir+".key")
		if err := os.WriteFile(keyPath, []byte(privateKeyPEM), 0o600); err != nil {
			return fmt.Errorf("write deploy key: %w", err)
		}
		defer os.Remove(keyPath)
		sshCmd = fmt.Sprintf(
			"ssh -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o BatchMode=yes",
			keyPath,
		)
	}

	args := []string{"clone", "--filter=blob:none", "--depth", "1", "--branch", branch, repoURL, destDir}
	if out, err := runGit(ctx, sshCmd, args...); err != nil {
		return fmt.Errorf("clone: %w: %s", err, out)
	}
	if commitSHA != "" {
		if out, err := runGit(ctx, sshCmd, "-C", destDir, "fetch", "--depth", "1", "origin", commitSHA); err != nil {
			return fmt.Errorf("fetch pinned commit: %w: %s", err, out)
		}
		if out, err := runGit(ctx, sshCmd, "-C", destDir, "checkout", "--detach", commitSHA); err != nil {
			return fmt.Errorf("checkout pinned commit: %w: %s", err, out)
		}
	}
	return nil
}

// Head returns the HEAD commit sha and message of a checkout.
func Head(ctx context.Context, dir string) (sha, message string, err error) {
	sha, err = runGitOut(ctx, "", "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	message, err = runGitOut(ctx, "", "-C", dir, "log", "-1", "--pretty=%B")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(sha), strings.TrimSpace(message), nil
}

func runGit(ctx context.Context, sshCmd string, args ...string) (string, error) {
	out, err := runGitOut(ctx, sshCmd, args...)
	return out, err
}

func runGitOut(ctx context.Context, sshCmd string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if sshCmd != "" {
		cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+sshCmd)
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// marshalEd25519PrivateKey encodes an ed25519 private key in the OpenSSH
// format git/ssh accept.
func marshalEd25519PrivateKey(key ed25519.PrivateKey) ([]byte, error) {
	// x/crypto/ssh's MarshalPrivateKey produces exactly the OPENSSH PRIVATE
	// KEY format we want.
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(block), nil
}

// kept for potential fallback use; GenerateDeployKey currently prefers the
// ssh-keygen CLI (see above).
var _ = marshalEd25519PrivateKey
