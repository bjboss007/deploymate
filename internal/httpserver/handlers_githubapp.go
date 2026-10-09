package httpserver

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitauth"
	"github.com/habibmuhammad/deploymate/internal/githubapp"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
	"github.com/habibmuhammad/deploymate/web/templates"
)

const ghStateCookie = "dm_gh_state"

var ghLoginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

// SetPublicHost tells the server the domain it is reachable on from the internet
// (DEPLOYMATE_DASHBOARD_HOST), used for the GitHub App's webhook address.
func (s *Server) SetPublicHost(host string) { s.publicHost = host }

// SetGitHubWeb overrides https://github.com (tests).
func (s *Server) SetGitHubWeb(u string) { s.githubWeb = u }

// publicBase is the address GitHub can reach this server on: the dashboard
// domain when one is configured, otherwise the address the request came in on if
// that is not a loopback or private one. ok is false when there is none.
func (s *Server) publicBase(r *http.Request) (base string, ok bool) {
	if s.publicHost != "" {
		return "https://" + s.publicHost, true
	}
	host := r.Host
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") {
		return "", false
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return "", false
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + host, true
}

// originOf is where this browser reached the dashboard, for the redirect back
// from GitHub (works on loopback too: the redirect happens in the browser).
func originOf(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// gitAuth is the credential resolver for git sources (built once: it caches tokens).
func (s *Server) gitAuth() *gitauth.Resolver {
	s.authOnce.Do(func() { s.auth = gitauth.New(s.store, s.encKey, s.githubAPI) })
	return s.auth
}

func (s *Server) ghClient() *githubapp.Client { return githubapp.New(s.githubAPI) }

func (s *Server) handleGitHubPage(w http.ResponseWriter, r *http.Request) {
	view := templates.GitHubView{}
	_, view.Public = s.publicBase(r)
	app, err := s.store.GetGitHubApp()
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		slog.Error("github: load app", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	default:
		view.Connected = true
		view.AppName, view.AppURL, view.Owner, view.OwnerType = app.Name, app.HTMLURL, app.OwnerLogin, strings.ToLower(app.OwnerType)
		view.WebhookURL = app.WebhookURL
		view.InstallURL = "https://github.com/apps/" + url.PathEscape(app.Slug) + "/installations/new"
		if s.githubWeb != "" {
			view.InstallURL = s.githubWeb + "/apps/" + url.PathEscape(app.Slug) + "/installations/new"
		}
		view.Installations, view.InstallError = s.listInstallations(r.Context(), app)
	}
	render(w, r, http.StatusOK, templates.GitHubPage(s.viewCtx(r), view))
}

func (s *Server) listInstallations(ctx context.Context, app store.GitHubApp) ([]templates.GitHubInstallation, string) {
	pemKey, err := crypto.Decrypt(s.encKey, app.PEMEnc)
	if err != nil {
		slog.Error("github: decrypt key", "err", err)
		return nil, "DeployMate could not read the app's key. Disconnect and connect again."
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ins, err := s.ghClient().ListInstallations(ctx, app.AppID, pemKey)
	if err != nil {
		slog.Warn("github: list installations", "err", err)
		var ae *githubapp.APIError
		if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 404) {
			return nil, "GitHub no longer accepts this app's key. The app may have been deleted on GitHub: disconnect and connect again."
		}
		return nil, "Could not ask GitHub where the app is installed right now. Try again in a moment."
	}
	var out []templates.GitHubInstallation
	for _, in := range ins {
		sel := "selected repositories"
		if in.RepositorySelection == "all" {
			sel = "all repositories"
		}
		out = append(out, templates.GitHubInstallation{Account: in.Account.Login, Type: strings.ToLower(in.Account.Type), Selection: sel, Suspended: in.SuspendedAt != ""})
	}
	return out, ""
}

// handleGitHubConnect starts the manifest flow: remember a state value in a cookie
// and hand the browser to GitHub with the manifest.
func (s *Server) handleGitHubConnect(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.GetGitHubApp(); err == nil {
		redirectWithFlash(w, r, "/settings/github", "GitHub is already connected.")
		return
	}
	org := ""
	if r.FormValue("kind") == "org" {
		org = strings.TrimSpace(r.FormValue("org"))
		if !ghLoginRE.MatchString(org) {
			redirectWithFlash(w, r, "/settings/github", "Enter the organisation's name as GitHub shows it.")
			return
		}
	}
	state, err := githubapp.NewState()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	origin := originOf(r)
	base, public := s.publicBase(r)
	home := base
	if !public {
		home = origin
	}
	name := "DeployMate"
	if s.publicHost != "" {
		name = "DeployMate " + s.publicHost
	} else if public {
		name = "DeployMate " + strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	} else if sfx, err := githubapp.NewState(); err == nil {
		name = "DeployMate " + sfx[:5]
	}
	webhook := ""
	if public {
		webhook = base + "/hooks/github-app"
	}
	manifest, err := githubapp.Manifest(githubapp.ManifestOpts{
		Name: name, HomepageURL: home, RedirectURL: origin + "/settings/github/callback",
		SetupURL: origin + "/settings/github/installed", WebhookURL: webhook,
	})
	if err != nil {
		slog.Error("github: manifest", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: ghStateCookie, Value: state, Path: "/settings/github", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r), MaxAge: 3600,
	})
	w.Header().Set("Cache-Control", "no-store")
	render(w, r, http.StatusOK, templates.GitHubRedirectPage(githubapp.NewFormURL(s.githubWeb, org, state), string(manifest)))
}

// handleGitHubCallback is where GitHub sends the browser back with a one-time code.
func (s *Server) handleGitHubCallback(w http.ResponseWriter, r *http.Request) {
	clear := func() {
		http.SetCookie(w, &http.Cookie{Name: ghStateCookie, Value: "", Path: "/settings/github", MaxAge: -1, HttpOnly: true})
	}
	c, err := r.Cookie(ghStateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || c.Value == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		clear()
		redirectWithFlash(w, r, "/settings/github", "That GitHub connection did not start here (or took over an hour). Press Connect GitHub again.")
		return
	}
	clear()
	if _, err := s.store.GetGitHubApp(); err == nil {
		redirectWithFlash(w, r, "/settings/github", "GitHub is already connected.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	cr, err := s.ghClient().ConvertManifest(ctx, r.URL.Query().Get("code"))
	if err != nil {
		slog.Warn("github: manifest conversion", "err", err) // the code itself is never logged
		redirectWithFlash(w, r, "/settings/github", "GitHub did not accept the setup. Press Connect GitHub to try again.")
		return
	}
	pemEnc, err1 := crypto.Encrypt(s.encKey, cr.PEM)
	whEnc, err2 := crypto.Encrypt(s.encKey, cr.WebhookSecret)
	csEnc, err3 := crypto.Encrypt(s.encKey, cr.ClientSecret)
	if err1 != nil || err2 != nil || err3 != nil {
		slog.Error("github: encrypt credentials")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	hook := ""
	if base, ok := s.publicBase(r); ok {
		hook = base + "/hooks/github-app"
	}
	if err := s.store.SaveGitHubApp(store.GitHubApp{
		AppID: cr.ID, Slug: cr.Slug, Name: cr.Name, HTMLURL: cr.HTMLURL, OwnerLogin: cr.Owner.Login, OwnerType: cr.Owner.Type,
		ClientID: cr.ClientID, ClientSecretEnc: csEnc, WebhookSecretEnc: whEnc, PEMEnc: pemEnc, WebhookURL: hook,
	}); err != nil {
		slog.Error("github: save app", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.store.RecordEvent("", store.EventGitHubConnected, "GitHub App "+cr.Name+" connected")
	redirectWithFlash(w, r, "/settings/github", "Connected. Now install the app on the repositories you want to deploy.")
}

// handleGitHubInstalled is where GitHub sends the browser after an installation.
func (s *Server) handleGitHubInstalled(w http.ResponseWriter, r *http.Request) {
	redirectWithFlash(w, r, "/settings/github", "Installed on GitHub.")
}

func (s *Server) handleGitHubDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteGitHubApp(); err != nil {
		slog.Error("github: disconnect", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.store.RecordEvent("", store.EventGitHubDisconnected, "GitHub disconnected")
	redirectWithFlash(w, r, "/settings/github", "Disconnected. Delete the app on GitHub too if you no longer want it.")
}

// handleGitHubAppWebhook is the GitHub App's single webhook. Public, so the
// signature is the only credential; phase 1 answers ping and acknowledges the rest.
func (s *Server) handleGitHubAppWebhook(w http.ResponseWriter, r *http.Request) {
	app, err := s.store.GetGitHubApp()
	if errors.Is(err, store.ErrNotFound) {
		notFoundPage(w, r)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	secret, err := crypto.Decrypt(s.encKey, app.WebhookSecretEnc)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !webhooks.VerifyGitHub(secret, r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping":
		_, _ = w.Write([]byte("pong"))
	case "push":
		push, err := webhooks.ParseGitHubPush(body)
		if err != nil {
			http.Error(w, "unparseable payload", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(s.handleAppPush(push, r.Header.Get("X-GitHub-Delivery"))))
	case "installation", "installation_repositories":
		// The repositories the app can see changed: let the picker ask GitHub again.
		s.repoMu.Lock()
		s.repoCacheV = repoCache{}
		s.repoMu.Unlock()
		_, _ = w.Write([]byte("ok"))
	case "workflow_run":
		_, _ = w.Write([]byte("ignored: prebuilt deploys through the GitHub app are not supported yet"))
	default:
		_, _ = w.Write([]byte("ignored: not a push event"))
	}
}

// handleAppPush queues deployments for the apps connected to the pushed
// repository and branch, and returns the answer GitHub's delivery log will show.
func (s *Server) handleAppPush(push webhooks.Push, delivery string) string {
	if push.Deleted {
		return "ignored: branch deleted"
	}
	sources, err := s.store.ListGitSourcesByRepo(push.RepoFullName)
	if err != nil {
		slog.Error("github webhook: list sources", "err", err)
		return "error"
	}
	if len(sources) == 0 {
		return "ignored: no app uses this repository"
	}
	branch := webhooks.BranchFromRef(push.Ref)
	var total pushOutcome
	matched, duplicates := 0, 0
	for _, gs := range sources {
		if gs.DefaultBranch != branch {
			continue
		}
		matched++
		if s.deliveries.Seen("github-app", gs.ID, delivery) {
			duplicates++
			continue
		}
		o := s.queuePushDeploys(gs, push)
		total.queued += o.queued
		total.ciOnly += o.ciOnly
		total.skipped += o.skipped
	}
	switch {
	case matched == 0:
		return "ignored: not the deploy branch"
	case total.queued > 0:
		return "queued"
	case duplicates == matched:
		return "duplicate delivery ignored"
	case total.ciOnly > 0:
		return "ignored: this app deploys from CI runs, not pushes"
	case total.skipped > 0:
		return "ignored: no changes in the build folder"
	}
	return "ignored: no app uses this repository"
}

func redirectWithFlash(w http.ResponseWriter, r *http.Request, path, msg string) {
	http.Redirect(w, r, path+"?flash="+url.QueryEscape(msg), http.StatusSeeOther)
}

// ---- repositories ------------------------------------------------------------

type repoCache struct {
	at    time.Time
	repos []templates.GitHubRepoOption
	err   string
}

const repoCacheTTL = time.Minute

// githubPick lists the repositories the app can be connected to, for the app
// page. It asks GitHub at most once a minute and never blocks the page for long.
// wanted is false when the app already has a repository (nothing to pick).
func (s *Server) githubPick(ctx context.Context, wanted bool) templates.GitHubPick {
	if !wanted {
		return templates.GitHubPick{}
	}
	app, err := s.store.GetGitHubApp()
	if err != nil {
		return templates.GitHubPick{}
	}
	pick := templates.GitHubPick{Connected: true}
	s.repoMu.Lock()
	if s.repoCacheV.at.After(time.Now().Add(-repoCacheTTL)) {
		c := s.repoCacheV
		s.repoMu.Unlock()
		pick.Repos, pick.Error = c.repos, c.err
		return pick
	}
	s.repoMu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	repos, msg := s.listGitHubRepos(ctx, app)
	s.repoMu.Lock()
	s.repoCacheV = repoCache{at: time.Now(), repos: repos, err: msg}
	s.repoMu.Unlock()
	pick.Repos, pick.Error = repos, msg
	return pick
}

func (s *Server) listGitHubRepos(ctx context.Context, app store.GitHubApp) ([]templates.GitHubRepoOption, string) {
	pemKey, err := crypto.Decrypt(s.encKey, app.PEMEnc)
	if err != nil {
		return nil, "DeployMate could not read the GitHub app's key. Disconnect and connect GitHub again."
	}
	cl := s.ghClient()
	ins, err := cl.ListInstallations(ctx, app.AppID, pemKey)
	if err != nil {
		slog.Warn("github: list installations for the picker", "err", err)
		return nil, "Could not ask GitHub for your repositories right now."
	}
	var out []templates.GitHubRepoOption
	for _, in := range ins {
		if in.SuspendedAt != "" {
			continue
		}
		tok, err := s.gitAuth().Tokens.Get(ctx, app.AppID, pemKey, in.ID)
		if err != nil {
			slog.Warn("github: installation token", "installation", in.ID, "err", err)
			continue
		}
		repos, err := cl.ListInstallationRepos(ctx, tok)
		if err != nil {
			slog.Warn("github: list repositories", "installation", in.ID, "err", err)
			continue
		}
		for _, r := range repos {
			if r.Archived {
				continue
			}
			out = append(out, templates.GitHubRepoOption{Value: strconv.FormatInt(in.ID, 10) + ":" + r.FullName, Label: r.FullName, Private: r.Private})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Label) < strings.ToLower(out[j].Label) })
	return out, ""
}

// connectGitHubRepoCore connects an app to a repository through the GitHub App.
// The repository must be one the installation can see, as GitHub says: the form
// value is the browser's word, not a permission. A non-empty refusal is for the person.
func (s *Server) connectGitHubRepoCore(ctx context.Context, app store.App, choice, branch string) (gs store.GitSource, refusal string, err error) {
	if app.GitSourceID != "" {
		return gs, "This app already has a repository connected.", nil
	}
	idStr, fullName, ok := strings.Cut(strings.TrimSpace(choice), ":")
	installID, perr := strconv.ParseInt(idStr, 10, 64)
	if !ok || perr != nil || installID <= 0 || !strings.Contains(fullName, "/") {
		return gs, "Pick a repository from the list.", nil
	}
	ghApp, err := s.store.GetGitHubApp()
	if errors.Is(err, store.ErrNotFound) {
		return gs, "GitHub is not connected.", nil
	}
	if err != nil {
		return gs, "", err
	}
	pemKey, err := crypto.Decrypt(s.encKey, ghApp.PEMEnc)
	if err != nil {
		return gs, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := s.gitAuth().Tokens.Get(ctx, ghApp.AppID, pemKey, installID)
	if err != nil {
		slog.Warn("github: installation token", "err", err)
		return gs, "GitHub would not let DeployMate's app in. Check that the app is still installed there.", nil
	}
	cl := s.ghClient()
	repos, err := cl.ListInstallationRepos(ctx, tok)
	if err != nil {
		slog.Warn("github: list repositories", "err", err)
		return gs, "Could not ask GitHub about that repository right now. Try again in a moment.", nil
	}
	var repo *githubapp.Repo
	for i := range repos {
		if strings.EqualFold(repos[i].FullName, fullName) {
			repo = &repos[i]
			break
		}
	}
	if repo == nil {
		return gs, "The GitHub app can't see that repository. Add it to the app's installation on GitHub.", nil
	}
	if !strings.HasPrefix(repo.CloneURL, "https://") {
		return gs, "GitHub returned a repository address DeployMate won't clone from.", nil
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		branch = repo.DefaultBranch
	}
	if branch == "" || len(branch) > 200 || strings.ContainsAny(branch, " \t\n~^:?*[\\") || strings.HasPrefix(branch, "-") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return gs, "That is not a valid branch name.", nil
	}
	exists, err := cl.BranchExists(ctx, tok, repo.FullName, branch)
	if err != nil {
		return gs, "Could not check the branch on GitHub right now. Try again in a moment.", nil
	}
	if !exists {
		return gs, "The branch \"" + branch + "\" does not exist in " + repo.FullName + ".", nil
	}
	secretEnc, err := crypto.Encrypt(s.encKey, randomHex(24))
	if err != nil {
		return gs, "", err
	}
	gs, err = s.store.CreateGitSource(store.GitSource{
		Provider: "github", RepoURL: repo.CloneURL, CloneMethod: store.CloneGitHubApp, WebhookSecretEnc: secretEnc,
		DefaultBranch: branch, InstallationID: installID, RepoFullName: strings.ToLower(repo.FullName),
	})
	if err != nil {
		return gs, "", err
	}
	if err := s.store.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		return gs, "", err
	}
	_ = s.store.RecordEvent(app.ID, store.EventGitConnected, "repository "+repo.FullName+" ("+branch+") connected through GitHub")
	return gs, "", nil
}

func (s *Server) handleGitHubRepoConnect(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	_, refusal, err := s.connectGitHubRepoCore(r.Context(), app, r.FormValue("repo"), r.FormValue("branch"))
	if err != nil {
		slog.Error("github: connect repository", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if refusal != "" {
		redirectWithFlash(w, r, "/apps/"+app.Slug, refusal)
		return
	}
	redirectWithFlash(w, r, "/apps/"+app.Slug, "Repository connected. Review & deploy to publish it.")
}
