package main

import (
	cryptoRand "crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/habibmuhammad/deploymate/internal/config"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// seedGitSource links an app to a git source pointing at an arbitrary repo
// URL — including a local bare-repo path the HTTP connect handler rejects
// (it accepts only ssh/https). This is a test-seeding primitive, the git
// equivalent of setup-admin: the e2e suite uses it to exercise the worker
// deploy path without a real forge. It generates a real deploy key (the
// worker decrypts it even for local clones) and a webhook secret, then
// prints the source ID and the plaintext secret so a caller can sign a
// webhook. Usage:
//
//	deploymate seed-git-source <app-slug> <repo-url> [branch] [provider]
//
// Prebuilt (GitHub Actions artifact) deploys are seeded through the
// environment, so the argument list above stays stable:
//
//	DEPLOYMATE_SEED_MODE=artifact        switch the app to prebuilt mode
//	DEPLOYMATE_SEED_API_TOKEN=<token>    store the (encrypted) GitHub token
//	DEPLOYMATE_SEED_WORKFLOW=<path>      workflow file (default: the app's)
//	DEPLOYMATE_SEED_ARTIFACT=<name>      artifact name (default: the app's)
func seedGitSource() error {
	args := flag.Args()[1:] // drop the subcommand itself
	if len(args) < 2 {
		return errors.New("usage: deploymate seed-git-source <app-slug> <repo-url> [branch] [provider]")
	}
	slug, repoURL := args[0], args[1]
	branch := "main"
	if len(args) >= 3 && args[2] != "" {
		branch = args[2]
	}
	provider := "github"
	if len(args) >= 4 && args[3] != "" {
		provider = args[3]
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	encKey, err := crypto.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return err
	}

	app, err := st.GetAppBySlug(slug)
	if err != nil {
		return fmt.Errorf("app %q: %w", slug, err)
	}

	key, err := gitpkg.GenerateDeployKey()
	if err != nil {
		return err
	}
	privEnc, err := crypto.Encrypt(encKey, key.PrivateKeyPEM)
	if err != nil {
		return err
	}
	secret := randHex(24)
	secretEnc, err := crypto.Encrypt(encKey, secret)
	if err != nil {
		return err
	}

	var tokenEnc string
	if tok := os.Getenv("DEPLOYMATE_SEED_API_TOKEN"); tok != "" {
		if tokenEnc, err = crypto.Encrypt(encKey, tok); err != nil {
			return err
		}
	}
	gs, err := st.CreateGitSource(store.GitSource{
		Provider:         provider,
		RepoURL:          repoURL,
		CloneMethod:      "deploy_key",
		PrivateKeyEnc:    privEnc,
		WebhookSecretEnc: secretEnc,
		DefaultBranch:    branch,
		APITokenEnc:      tokenEnc,
	})
	if err != nil {
		return err
	}
	if err := st.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		return err
	}
	if mode := os.Getenv("DEPLOYMATE_SEED_MODE"); mode != "" {
		workflow := os.Getenv("DEPLOYMATE_SEED_WORKFLOW")
		if workflow == "" {
			workflow = app.WorkflowPath
		}
		artifact := os.Getenv("DEPLOYMATE_SEED_ARTIFACT")
		if artifact == "" {
			artifact = app.ArtifactName
		}
		if err := st.UpdateAppDeployMode(app.ID, mode, workflow, artifact); err != nil {
			return err
		}
	}

	// Machine-readable output for the e2e script (key=value lines).
	fmt.Printf("source_id=%s\n", gs.ID)
	fmt.Printf("webhook_secret=%s\n", secret)
	return nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := cryptoRand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
