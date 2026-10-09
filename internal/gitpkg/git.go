// Package gitpkg wraps the git CLI for clone and deploy-key operations.
// DeployMate shells out to git rather than linking libgit2: the CLI is
// ubiquitous, handles SSH natively, and partial clones keep checkouts small.
package gitpkg

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/pem"
	"net/url"
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

// Auth is how git proves itself to the remote: an SSH deploy key for ssh URLs, or
// an HTTPS access token (a GitHub App installation token) for https URLs.
type Auth struct {
	KeyPEM string
	Token  string
}

// env returns the environment git needs for repoURL. A token goes in an HTTP
// header passed through GIT_CONFIG_* variables, never in the URL or argv, so it
// is not written to .git/config nor shown by ps.
func (a Auth) env(repoURL string) []string {
	if a.Token == "" {
		return nil
	}
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil // a token is never sent to a non-https remote
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + a.Token))
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + u.Scheme + "://" + u.Host + "/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
	}
}

// Clone checks out repoURL into destDir. branch is required; commitSHA, when
// non-empty, pins the checkout to that commit. SSH URLs (git@…, ssh://…)
// authenticate with the deploy key written to a 0600 temp file; HTTPS URLs
// clone without a key (public repos, or PAT-in-URL later).
func Clone(ctx context.Context, repoURL, branch, commitSHA, privateKeyPEM, destDir string) error {
	return CloneAuth(ctx, repoURL, branch, commitSHA, Auth{KeyPEM: privateKeyPEM}, destDir)
}

// CloneAuth is Clone with an explicit credential (deploy key or access token).
func CloneAuth(ctx context.Context, repoURL, branch, commitSHA string, auth Auth, destDir string) error {
	privateKeyPEM := auth.KeyPEM
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

	env := auth.env(repoURL)
	args := []string{"clone", "--filter=blob:none", "--depth", "1", "--branch", branch, repoURL, destDir}
	if out, err := runGit(ctx, sshCmd, env, args...); err != nil {
		return fmt.Errorf("clone: %w: %s", err, out)
	}
	if commitSHA != "" {
		if out, err := runGit(ctx, sshCmd, env, "-C", destDir, "fetch", "--depth", "1", "origin", commitSHA); err != nil {
			return fmt.Errorf("fetch pinned commit: %w: %s", err, out)
		}
		if out, err := runGit(ctx, sshCmd, env, "-C", destDir, "checkout", "--detach", commitSHA); err != nil {
			return fmt.Errorf("checkout pinned commit: %w: %s", err, out)
		}
	}
	return normalizeModes(destDir)
}

// normalizeModes makes a checkout readable by whoever the built image runs as.
// DeployMate's service runs with a 0077 umask, so git writes files 0600 and
// directories 0700; a Dockerfile COPY keeps those modes, and a container running
// as a non-root user (nginx, node, most official images) then cannot read its own
// app — a 403 or "permission denied" that has nothing to do with the code. Git
// itself only records "executable or not", so rebuilding the modes loses nothing:
// directories 0755, executable files 0755, the rest 0644. .git is left alone.
func normalizeModes(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // never chmod through a link
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if d.IsDir() || info.Mode()&0o100 != 0 {
			mode = 0o755
		}
		if info.Mode().Perm() == mode {
			return nil
		}
		return os.Chmod(path, mode)
	})
}

// Head returns the HEAD commit sha and message of a checkout.
func Head(ctx context.Context, dir string) (sha, message string, err error) {
	sha, err = runGitOut(ctx, "", nil, "-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	message, err = runGitOut(ctx, "", nil, "-C", dir, "log", "-1", "--pretty=%B")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(sha), strings.TrimSpace(message), nil
}

func runGit(ctx context.Context, sshCmd string, env []string, args ...string) (string, error) {
	out, err := runGitOut(ctx, sshCmd, env, args...)
	return out, err
}

// runGitOut runs git with an optional SSH command and extra environment.
func runGitOut(ctx context.Context, sshCmd string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if sshCmd != "" || len(env) > 0 {
		cmd.Env = os.Environ()
		if sshCmd != "" {
			cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+sshCmd)
		}
		cmd.Env = append(cmd.Env, env...)
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
