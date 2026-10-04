package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// The provisioning cores: create and configure things. Each returns the new
// object, a refusal sentence when the request is not acceptable (the reason a
// person — or an agent — can act on), or an error for storage failures. The
// dashboard forms and the API both call them, so a rule is written once.

func validEnvironment(env string) bool {
	return env == store.EnvDev || env == store.EnvStaging || env == store.EnvProduction
}

func (s *Server) createProjectCore(userID, name string) (store.Project, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return store.Project{}, "Project name must be 1-64 characters.", nil
	}
	slug := slugify(name)
	if slug == "" {
		return store.Project{}, "Project name has no usable characters.", nil
	}
	p, err := s.store.CreateProject(store.Project{UserID: userID, Name: name, Slug: slug})
	if err != nil {
		slog.Error("projects: create", "err", err)
		return store.Project{}, "Could not create project (slug may already exist).", nil
	}
	return p, "", nil
}

// createAppCore creates an app. warning is set when the app exists but its
// preview DNS record could not be created (it never fails creation).
func (s *Server) createAppCore(ctx context.Context, project store.Project, name, env string) (app store.App, warning, refusal string, err error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return app, "", "App name must be 1-64 characters.", nil
	}
	slug := slugify(name)
	if slug == "" {
		return app, "", "App name has no usable characters.", nil
	}
	if env != "" && !validEnvironment(env) {
		return app, "", "Unknown environment — use dev, staging or production.", nil
	}
	app, err = s.store.CreateApp(store.App{
		ProjectID: project.ID, Name: name, Slug: slug, Environment: env,
		Status: "stopped", BuildType: "dockerfile", Port: 8080,
	})
	if errors.Is(err, store.ErrSlugTaken) {
		return app, "", "That name is already taken.", nil
	}
	if err != nil {
		return app, "", "", err
	}
	// Best-effort auto-DNS: the preview CNAME lets Cloudflare's free plan
	// issue a per-app edge cert. Single attempt, 5s bound (unlike alerts,
	// a retry buys little — the record can be created manually), and it
	// never fails app creation.
	if s.dns != nil && s.previewHost != "" {
		host := app.Slug + "." + s.previewHost
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		derr := s.dns.EnsurePreviewRecord(dctx, host)
		cancel()
		if derr != nil {
			slog.Warn("apps: preview dns", "app", app.Slug, "host", host, "err", derr)
			_ = s.store.RecordEvent(app.ID, store.EventDNSRecordFailed, host+": "+derr.Error())
			warning = "App created — but its preview DNS record could not be created automatically; the preview URL will not get a certificate until the record exists."
		}
	}
	return app, warning, "", nil
}

func (s *Server) createServiceCore(project store.Project, name, svcType, env string) (store.Service, string, error) {
	name = strings.TrimSpace(name)
	tpl, known := services.ForType(svcType)
	if name == "" || len(name) > 64 {
		return store.Service{}, "Service name must be 1-64 characters.", nil
	}
	if !known {
		return store.Service{}, "Unknown service type.", nil
	}
	slug := slugify(name)
	if slug == "" {
		return store.Service{}, "Service name has no usable characters.", nil
	}
	if env == "" {
		env = store.EnvProduction // forms and clients that don't send one keep the old behaviour
	}
	if !validEnvironment(env) {
		return store.Service{}, "Unknown environment.", nil
	}
	svc, err := s.store.CreateService(store.Service{
		ProjectID: project.ID, Type: svcType, Name: name, Slug: slug, Environment: env,
		Image: tpl.Image, Status: "stopped", VolumeName: services.VolumeName(slug), Port: tpl.Port,
	})
	if errors.Is(err, store.ErrSlugTaken) {
		return store.Service{}, "That name is already taken.", nil
	}
	if err != nil {
		return store.Service{}, "", err
	}
	return svc, "", nil
}

// connectRepoCore links a git repository to the app: it generates the deploy
// key and the webhook secret and returns the key's PUBLIC half (to be added to
// the repository). It refuses an app that already has a repository — replacing
// one would orphan its deploy key and webhook, which is a dashboard decision.
func (s *Server) connectRepoCore(app store.App, repoURL, provider, branch string) (gs store.GitSource, publicKey, refusal string, err error) {
	repoURL = strings.TrimSpace(repoURL)
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = "main"
	}
	if app.GitSourceID != "" {
		return gs, "", "This app already has a repository connected — change it from the dashboard.", nil
	}
	if repoURL == "" ||
		!(strings.HasPrefix(repoURL, "git@") || strings.HasPrefix(repoURL, "ssh://") || strings.HasPrefix(repoURL, "https://")) {
		return gs, "", "Repo URL must be SSH (git@github.com:you/repo.git) or HTTPS (https://github.com/you/repo.git)", nil
	}
	if provider != "github" && provider != "gitlab" && provider != "gitea" {
		return gs, "", "Unknown provider.", nil
	}
	key, err := gitpkg.GenerateDeployKey()
	if err != nil {
		return gs, "", "", err
	}
	privEnc, err := crypto.Encrypt(s.encKey, key.PrivateKeyPEM)
	if err != nil {
		return gs, "", "", err
	}
	secretEnc, err := crypto.Encrypt(s.encKey, randomHex(24))
	if err != nil {
		return gs, "", "", err
	}
	gs, err = s.store.CreateGitSource(store.GitSource{
		Provider: provider, RepoURL: repoURL, CloneMethod: "deploy_key",
		PrivateKeyEnc: privEnc, WebhookSecretEnc: secretEnc, DefaultBranch: branch,
	})
	if err != nil {
		return gs, "", "", err
	}
	if err := s.store.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		return gs, "", "", err
	}
	pub, _ := gitpkg.PublicKeyFromPEM(key.PrivateKeyPEM)
	return gs, pub, "", nil
}

func (s *Server) addDomainCore(app store.App, hostname string) (store.Domain, string, error) {
	hostname = strings.ToLower(strings.TrimSpace(hostname))
	if hostname == "" || len(hostname) > 253 || !hostnameRe.MatchString(hostname) {
		return store.Domain{}, "That is not a valid hostname.", nil
	}
	d, err := s.store.CreateDomain(store.Domain{AppID: app.ID, Hostname: hostname, TLSStatus: "pending"})
	if err != nil {
		slog.Error("domains: create", "err", err)
		return store.Domain{}, "Could not add domain (already in use?).", nil
	}
	return d, "", nil
}

// upsertEnvItems stores variables (encrypted; sensitive-looking names are
// always masked), updating existing keys in place. A key repeated in items
// keeps its last value. It returns the names in order and how many were new.
func (s *Server) upsertEnvItems(app store.App, items []envItem) (names []string, added, updated int, err error) {
	existing, err := s.store.ListEnvVars(app.ID)
	if err != nil {
		return nil, 0, 0, err
	}
	had := map[string]bool{}
	for _, e := range existing {
		had[e.Key] = true
	}
	last := map[string]envItem{}
	for _, it := range items {
		if _, dup := last[it.Key]; !dup {
			names = append(names, it.Key)
		}
		last[it.Key] = it
	}
	for _, key := range names {
		it := last[key]
		valueEnc, err := crypto.Encrypt(s.encKey, it.Value)
		if err != nil {
			return nil, 0, 0, err
		}
		if _, err := s.store.UpsertEnvVar(store.EnvVar{
			AppID: app.ID, Key: key, ValueEnc: valueEnc, IsSecret: it.Secret || looksSecret(key),
		}); err != nil {
			return nil, 0, 0, err
		}
		if had[key] {
			updated++
		} else {
			added++
		}
	}
	// Names only — never values — go in the event log.
	_ = s.store.RecordEvent(app.ID, store.EventEnvChanged, "env vars set: "+truncate(strings.Join(names, ", "), 300))
	return names, added, updated, nil
}
