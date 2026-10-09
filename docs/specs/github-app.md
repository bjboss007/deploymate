# Connect GitHub (GitHub App)

Replaces, for GitHub only, the manual per-repository setup (deploy key, webhook URL and secret, and a
personal access token for prebuilt apps) with one GitHub App that this DeployMate registers on GitHub
through the manifest flow. **Both stay**: the deploy-key and webhook flow keeps working for GitHub, GitLab
and Gitea; Connect GitHub is an addition. Designed 2026-10-09.

## Spike (2026-10-09, GitHub docs; nothing was created on a live account)

Confirmed from the docs:
- The manifest is POSTed by the browser as a form field `manifest` (JSON string) to
  `https://github.com/settings/apps/new?state=…` (personal) or
  `https://github.com/organizations/{org}/settings/apps/new?state=…`. `state` comes back unchanged.
- Manifest keys used: `name` (≤34 chars, unique on GitHub; the person can edit it), `url`,
  `hook_attributes{url,active}`, `redirect_url`, `setup_url`, `public:false`, `description`,
  `default_permissions`, `default_events`.
- GitHub redirects the browser to `redirect_url?code=…&state=…`; the code must be exchanged within an hour:
  `POST /app-manifests/{code}/conversions` — **no authentication** (the code is the credential) — returns
  `id, slug, name, html_url, client_id, client_secret, webhook_secret, pem, owner{login,type}`.
- An app proves itself with an RS256 JWT (`iss` = app id, ≤10 min); installation tokens
  (`POST /app/installations/{id}/access_tokens`, JWT auth) last one hour. Since April 2026 GitHub is
  rolling out a longer, stateless token format: never assume 40 characters.
- Permissions requested: `contents: read` (clone / archive), `metadata: read`, `actions: write` (read runs and
  artifacts, dispatch or re-run a workflow). Events subscribed: `push`, `workflow_run`. The `installation`
  and `installation_repositories` events need no subscription (GitHub sends them to every app) — not
  re-verified in the docs read.

Not verified yet: creating an app on a real account (needs the owner's click — first real run is the owner's);
`GET /installation/repositories` fields (phase 2); the exact permission needed for each webhook event.

## Phase 1 — Connect (done 2026-10-09)

- `internal/githubapp`: `Manifest`, `NewFormURL`, `NewState`, `Client.ConvertManifest`, `AppJWT`/`ParseKey`
  (PKCS#1 and PKCS#8), `Client.ListInstallations`, `VerifyAppJWT` (used by the test GitHub).
- Table `github_app` (migration 0023, one row): app id, slug, name, owner, client id, and the private key,
  webhook secret and client secret **encrypted** with the instance key.
- Pages (session-protected, CSRF on POSTs): `GET /settings/github` (connect form, or the connected app, where
  it is installed, disconnect), `POST /settings/github/connect` (sets an HttpOnly `dm_gh_state` cookie and
  returns a page that submits the manifest to GitHub), `GET /settings/github/callback` (state must match the
  cookie; exchanges the code; stores the app; the code is never logged), `GET /settings/github/installed`
  (the manifest's `setup_url`), `POST /settings/github/disconnect`. Top-bar icon.
- `POST /hooks/github-app`: the app's single webhook (public; HMAC signature with the app's secret).
  Phase 1 answers `ping` with `pong` and acknowledges everything else.
- The webhook address is `DEPLOYMATE_DASHBOARD_HOST` or, failing that, the address the browser used if it is
  public. On a loopback/private address the app is created with its webhook **inactive** and the page says so.
- Tests: manifest contents and least privilege, form URLs, JWT round trip/tamper/expiry, a fake GitHub for the
  whole flow (wrong or missing state stores nothing, stale code stores nothing, secrets not in plaintext or on
  the page, second connect refused, disconnect), webhook signature, pages need a session.

## Phase 2 — Repositories and installation tokens (done 2026-10-09)

- Migration 0024: `git_sources.installation_id` and `repo_full_name` (lower case, indexed); clone method
  `github_app` (`store.CloneGitHubApp`). Such a source has no deploy key; its webhook secret is random and unused.
- `githubapp.Tokens`: installation tokens cached per installation (replaced 5 minutes before expiry; never
  reused across app ids). `ListInstallationRepos` (≤1000 repositories), `BranchExists`; owner, repository and
  branch names are validated before they reach an API path (no `..`, no empty segments).
- `gitpkg.Auth{KeyPEM, Token}` with `CloneAuth`, `MirrorSyncAuth`, `MirrorEnsureSHAAuth` (the old functions are
  wrappers). The token reaches git as an `Authorization: Basic` header through `GIT_CONFIG_*` environment
  variables for https remotes only, so it is not in the URL, argv, `.git/config` or the error text (tested
  against a local TLS server).
- `internal/gitauth.Resolver.For(source)`: deploy key as before, or an installation token for `github_app`
  sources, with plain errors when GitHub was disconnected or the app was uninstalled from the repository. The
  deployment worker and the deploy-review page use it.
- App page → Git: with GitHub connected, a "From GitHub" picker lists every non-archived repository of every
  (non-suspended) installation (cached a minute) plus an optional branch (default: the repository's default
  branch). `POST /apps/{slug}/git/github` accepts `installation:owner/name`, re-checks it against what
  GitHub says the installation can see, verifies the branch exists, and creates the source. The manual
  deploy-key form stays below it; connected apps show "Through your GitHub app" instead of key and webhook,
  and the first-deploy guide has no key step.
- Not done in this phase: pushes to a GitHub-app repository do not deploy yet (phase 3), the prebuilt-deploy
  panel is hidden for these sources (phase 4), and repositories added later are only seen after the cache
  minute (the `installation_repositories` event comes with phase 3).
- Unverified against real GitHub: `GET /installation/repositories`, `GET /repos/{o}/{r}/branches/{b}` and the
  token endpoint's exact shapes (written from GitHub's documented behaviour, tested against a fake).

## Phase 3 — App-level webhook handling

`push` → apps linked to that repository and branch (existing per-folder filtering applies), `workflow_run` →
prebuilt apps, `installation*` → repository list. Delivery de-duplication as for per-source hooks.

## Phase 4 — Prebuilt apps and fallbacks

Use the installation token for "Run workflow now" and the artifact download instead of a pasted PAT, offer
converting an existing deploy-key app, docs, site guide.

## Security notes

- The private key is the powerful secret (it mints installation tokens); it is encrypted at rest, never
  returned by any page or API, and `Disconnect` deletes it. Deleting the app on GitHub revokes it for good.
- The state cookie is HttpOnly, SameSite=Lax (sent on GitHub's top-level redirect), Secure behind HTTPS, one
  hour, and compared in constant time. The callback needs a signed-in dashboard session as well.
- The webhook is the only unauthenticated surface; it checks the signature before reading anything else.
