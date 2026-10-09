// Package gitauth turns a git source into the credential git needs: its deploy
// key, or, for a source created through Connect GitHub, a short-lived
// installation token minted from the connected GitHub App.
package gitauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/githubapp"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// ErrNotConnected means the source needs the GitHub App but it is no longer connected.
var ErrNotConnected = errors.New("this repository was connected through GitHub, which is no longer connected to DeployMate: connect GitHub again, or switch the app to a deploy key")

// Resolver builds credentials for git sources.
type Resolver struct {
	Store  *store.Store
	EncKey [32]byte
	Tokens *githubapp.Tokens
}

// New builds a Resolver whose GitHub calls go to api ("" = github.com).
func New(st *store.Store, encKey [32]byte, api string) *Resolver {
	return &Resolver{Store: st, EncKey: encKey, Tokens: githubapp.NewTokens(githubapp.New(api))}
}

// For returns the credential for a source.
func (r *Resolver) For(ctx context.Context, gs store.GitSource) (gitpkg.Auth, error) {
	if gs.CloneMethod != store.CloneGitHubApp {
		key, err := crypto.Decrypt(r.EncKey, gs.PrivateKeyEnc)
		if err != nil {
			return gitpkg.Auth{}, fmt.Errorf("decrypt deploy key: %w", err)
		}
		return gitpkg.Auth{KeyPEM: key}, nil
	}
	app, err := r.Store.GetGitHubApp()
	if errors.Is(err, store.ErrNotFound) {
		return gitpkg.Auth{}, ErrNotConnected
	}
	if err != nil {
		return gitpkg.Auth{}, err
	}
	pemKey, err := crypto.Decrypt(r.EncKey, app.PEMEnc)
	if err != nil {
		return gitpkg.Auth{}, fmt.Errorf("decrypt GitHub App key: %w", err)
	}
	tok, err := r.Tokens.Get(ctx, app.AppID, pemKey, gs.InstallationID)
	if err != nil {
		var ae *githubapp.APIError
		if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 404) {
			return gitpkg.Auth{}, errors.New("GitHub no longer lets DeployMate's app reach this repository: the app was uninstalled from it or deleted on GitHub")
		}
		return gitpkg.Auth{}, fmt.Errorf("ask GitHub for an access token: %w", err)
	}
	return gitpkg.Auth{Token: tok}, nil
}

// ErrNoToken means a deploy-key source has no GitHub API token saved.
var ErrNoToken = errors.New("no GitHub token is set for this repository")

// APIToken returns the token for GitHub REST calls about the source's repository
// (Actions runs, artifacts, dispatching a workflow): an installation token for a
// source connected through GitHub, otherwise the fine-grained token the owner
// pasted, or ErrNoToken.
func (r *Resolver) APIToken(ctx context.Context, gs store.GitSource) (string, error) {
	if gs.CloneMethod == store.CloneGitHubApp {
		a, err := r.For(ctx, gs)
		return a.Token, err
	}
	if gs.APITokenEnc == "" {
		return "", ErrNoToken
	}
	tok, err := crypto.Decrypt(r.EncKey, gs.APITokenEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt GitHub token: %w", err)
	}
	return tok, nil
}
