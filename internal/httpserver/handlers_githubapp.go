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
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
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
	switch r.Header.Get("X-GitHub-Event") {
	case "ping":
		_, _ = w.Write([]byte("pong"))
	default:
		_, _ = w.Write([]byte("ignored: not handled yet"))
	}
}

func redirectWithFlash(w http.ResponseWriter, r *http.Request, path, msg string) {
	http.Redirect(w, r, path+"?flash="+url.QueryEscape(msg), http.StatusSeeOther)
}
