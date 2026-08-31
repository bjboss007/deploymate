# Progress — DeployMate

> **Handoff file.** First thing any agent reads; last thing updated before
> stopping. Read this whole file (~1 minute), then follow the pointers for
> the task at hand — do **not** scan the repo to orient. If a pointer below
> doesn't answer something, read `docs/knowledge/architecture.md` next.

## Where we stopped

**2026-08-30 (e2e-git round)** — **Automated the git-deploy path** (Next-up
item 1, first slice). `make e2e-git` (`testdata/e2e_git.sh`) runs the real
product flow end to end against a **throwaway** server (port 18091, scratch
data dir, own owner) so it never touches the live :8090 apps: builds a local
bare repo from a new `testdata/repos/e2e-web` fixture (nginx Dockerfile on
the platform port 8080), links it, fires a **signed GitHub webhook**, waits
for the worker to clone→build→swap, then HTTP-probes the app through
`/preview/{slug}/`. Two enablers: a new **`seed-git-source` subcommand**
(`cmd/deploymate/seed.go`, git twin of `setup-admin`) that links an app to
any repo URL — including a local bare path the HTTP connect handler rejects —
and prints `source_id=`/`webhook_secret=` so the script can sign the hook;
and a **timestamped app slug** (`e2eweb<epoch>`) so the shared Docker daemon
never collides with the user's real `dm-*` containers. Passes no Cloudflare
vars (no real DNS records), uses the Dockerfile engine (no buildkit/railpack
dep). **E2e-verified**: full run PASSED (webhook→build→run→proxy served
"deploymate e2e git fixture"); teardown clean (no leftover server, container,
image, or temp repos; :8090 untouched). `make test` + `make vet` green. Docs
updated (dev-environment.md new section, improvements.md, Makefile).
**Gotcha found:** `go build ./cmd/deploymate` writes `./deploymate`, NOT
`bin/deploymate` — a stale `bin/` binary silently ran the old code (fell
through to `serve`, hung the seed step). The script now always `make build`s
unless `DM_BIN` is set. **Bigger latent bug found:** `.gitignore` had a bare
`deploymate` line (for the built binary) that also matched the
`cmd/deploymate/` **source dir** — the entire main package (`main.go`,
`serve`, `setupAdmin`) was **never committed**; a fresh clone would not
build. Anchored it to `/deploymate` so only the root binary is ignored;
`cmd/` is now trackable. **Not yet committed** (this round adds `cmd/` to
the repo for the first time — review the diff before pushing).

**2026-08-30 (logs round)** — **`build_logs` retention + app-log history
(stateless snapshot)**. Two backlog items:
1. **`build_logs` retention** — build logs (one row per output line, every
   deployment) accumulated forever. `Store.PruneBuildLogsBefore(before)`
   deletes lines older than 30 d; wired into the monitor's hourly `prune()`
   next to metrics/uptime (`buildLogsRetain = 30*24h`,
   `internal/monitor/monitor.go`). Deployment rows stay — only their verbose
   output is pruned. Unit-tested (`internal/store/build_logs_test.go`,
   future/past cutoff boundaries).
2. **App log history** — the log panel was live-only: a stopped app showed
   just "waiting for the container". The user chose the **stateless docker
   tail** over a DB ring buffer ("the db thing is overkill" for one
   maintainer). `handleAppLogs` now, when the container is **stopped but
   not removed**, emits a non-follow `docker logs` tail (200 lines) so you
   see *why* it went down. The stdcopy demux is extracted into a shared
   `writeContainerLogs` helper (unit-tested via a synthetic stdcopy stream).
   **Limitation by design:** a *removed* container (between deploys) has no
   docker logs, so pre-recreate history isn't shown — persisting across
   recreation would need the DB ring, backlogged as "App log persistence
   across recreation".
`go build`, full `make test`, and `make vet` all green. Docs updated
(both backlog items). **E2e-verified** on a throwaway server (:18097,
scratch data dir): deployed `nginx:alpine`, stopped it (container →
`exited`, not removed), hit `/apps/web/logs` → the SSE stream replayed the
container's nginx startup lines (correctly demuxed, stderr tagged
`[stderr]`) followed by the "waiting" message — before this change a
stopped app showed only "waiting". Then `docker rm -f dm-web` and re-hit
`/logs` → clean "waiting" only (no snapshot, no error), confirming the
removed-container degrade path. Throwaway server + container cleaned up;
the user's :8090 server + 9 apps untouched. `build_logs` retention is
unit-tested only (a 30 d window isn't practical to e2e). **Committed +
pushed** (bjboss007 rule).

**2026-08-30 (dev-environment round)** — **Added `dev` as a third app
environment** alongside staging/production, and made it the **default for
newly-created apps** (was production). Dev behaves exactly like staging:
its own env-prefixed services (`dev-postgres`, name "Dev PostgreSQL",
volume `dm-svc-dev-postgres-data`), its own `deploymate.dev.yml` overlay,
full isolation. Changes: `EnvDev` constant (`store/store.go`); default
flip in `store.CreateApp` (`store/apps.go`); validation gate + message in
`handlers_environment.go`; the provisioner's hardcoded "Staging "
service-label generalized to a capitalized env name
(`services/provisioner.go`) so any non-prod env names itself; UI selector
+ new blue `.badge-dev` tint (`web/templates/apps.templ`,
`web/static/app.css`, `make gen` run). No migration needed — the
`environment` column has no CHECK constraint (0009 default stays
`production`, a fallback the store overrides). New tests: dev cases in
provisioner/manifest/handler env-filter tests + a new
`handlers_environment_test.go` (accepts dev, rejects unknown). `go build`,
full `go test ./...`, and `make gen` all green. Docs updated (ADR 0017
addendum, infra-manifest.md). **E2e-verified** on a throwaway server
(:18099, scratch data dir, local bare repo of `manifest-app` with a
`deploymate.dev.yml` overlay): a new app defaulted to `dev` (UI
`badge-dev` + selector, DB `web|dev`); the git deploy logged
`manifest (dev): postgres` and auto-provisioned **Dev PostgreSQL /
`dev-postgres`** (origin manifest, volume `dm-svc-dev-postgres-data`,
image `postgres:16-alpine` from the overlay replacing the base's
unpinned postgres); after selecting the Node runtime the app built +
ran with `DATABASE_URL=…@dm-svc-dev-postgres:5432/app` injected; a second
deploy logged `manifest: reusing existing postgres service` (idempotent —
one service, no duplicate). Throwaway server + containers + volume +
build images all cleaned up; the user's :8090 server and apps untouched.
**Committed + pushed** as `a48665c` (bjboss007 rule); working tree clean.

**2026-08-30 (auto-DNS round)** — **Auto-DNS shipped and live-verified**.
New `internal/dns` package: `Creator` interface + Cloudflare API client
(`POST /zones/{id}/dns_records`, proxied CNAME to
`{tunnel-id}.cfargotunnel.com`, error 81053 = already-exists = success,
5s timeout, no retry). Config: `DEPLOYMATE_CLOUDFLARE_API_TOKEN` /
`_ZONE_ID` / `_TUNNEL_ID` + `CloudflareEnabled()`; `handleAppCreate` now
best-effort-creates the record (warning flash + `dns_record_failed`
event on failure; never fails creation; nil creator = today's behavior).
`previewURL` stays `http://` — cert issuance lags creation. Also fixed:
the auto-heal unit tests probed the deterministic preview port for slug
`py-api`, which the **running** py-api container now occupies → probes
succeeded and heal never ran; added `probeURLFn` override, tests pin to
`127.0.0.1:1`. `make test` + `make vet` green. Committed + pushed
(bjboss007 rule). **E2e-verified live** on :8090 (server restarted with
the three `DEPLOYMATE_CLOUDFLARE_*` vars — token/zone live in the
user's ~/.zshrc as `CLOUD_FLARE_TOKEN`/`CLOUD_FLARE_ZONE_ID`): created
test app "AutoDNS Check" through the real router → clean 303, no flash,
record `autodns-check.dm.getmerchanttech.com → {tunnel}.cfargotunnel.com
proxied=true` confirmed via the CF API; delete + recreate same name →
clean 303 (81053 idempotency live); test app + session cleaned up.
Earlier in the session: all 6 running apps got manual per-app DNS
records (`cloudflared tunnel route dns`) to restore https previews —
edge certs still provisioning (monitor watches `https://{slug}.dm…`).

**2026-08-30 (preview-url round)** — **Subdomain previews back, over plain
http**. The Access panel's preview URL was hardcoded `https://{slug}.
{previewHost}` (`previewURL`, `handlers_preview.go`), but Cloudflare's
free plan issues no edge cert for the `*.dm.getmerchanttech.com` wildcard
— TLS handshakes fail until each slug has its own DNS record
(cloudflare-tunnel.md documents this), so previews had fallen back to
`http://localhost:8090/preview/{slug}` (env var unset). Plain http on
the subdomain always worked. Now: `previewURL` emits `http://` for
preview subdomains (comment + spec + backlog note to flip back to https
once auto-DNS adds per-app records); `make dev` sets
`DEPLOYMATE_PREVIEW_HOST=dm.getmerchanttech.com`; restarted :8090 (pid
26626) with the var set. E2e-verified through the live tunnel:
`http://react-spa.dm.getmerchanttech.com` → 200 serving the React app,
dashboard 303 (login), `/preview/{slug}` still works. `make test` +
`make vet` green. Committed + pushed (bjboss007 rule).

**2026-08-30 (late)** — **Preview proxy fixes** (SPAs rendered blank
under `/preview/{slug}`). Round 1 (base-tag injection) was wrong —
absolute-path URLs replace a `<base>`'s path, proven by a real Chrome
reproduction (script still 404'd). Round 2, the real fix: the proxy
rewrites `src="/…"`/`href="/…"` in proxied HTML to carry the
`/preview/{slug}/` prefix, and `/preview/{slug}` 307-redirects to the
trailing-slash form so relative URLs resolve inside the app's own
directory. The demo app was updated to the canonical subpath pattern
(vite `base: "./"` + relative `fetch`). Verified in real headless
Chrome: login → `/preview/react-spa/` renders the React SPA, body text
present, `api/ping` → pong. `TestPreviewProxyRewritesURLs` covers the
rewrite (absolutes rewritten; protocol-relative, already-prefixed, and
root-path requests untouched). Caught by the react-spa demo the user
opened.

**2026-08-30 (late)** — **Frontend-framework proof + worker health fix**.
Deployed a React 18 + Vite 6 SPA (`react-spa`, production) through the
full git pipeline as proof that frontend frameworks work: fixture at
`/tmp/dm-react-spa.git` (bare) + `/tmp/dm-react-spa/` (source), app +
git_source rows in SQLite with an encrypted webhook secret + deploy key
(mirrors the UI connect flow — the worker decrypts the key even when a
local-path clone doesn't use it), deploy triggered by a signed GitHub
push payload to `/hooks/{id}`. Railpack detected node (`.mise.toml`
node 22), ran `npm run build` (vite, 144 kB bundle), started
`node server.js` on `$PORT=3000`; verified over the preview port
`127.0.0.1:27002`: SPA HTML + bundle 200, `/api/ping` →
`{"pong":"pong","port":3000}`, health green, idempotent redeploy via a
second webhook. Found + fixed en route: the worker's `finish` didn't
reset `apps.health` (dashboard deploys do) — now sets `healthy` on
success. The `react-spa` app stays in the dev DB as a demo; source
lives at `/tmp/dm-react-spa/`.

**2026-08-30 (evening)** — **Auto-heal wave** shipped and e2e-verified
live on the running :8090 server (all 5 apps healthy). Follow-up to the
morning's health/port bugfix wave (`5d3ffec`, already committed) which
closed three backlog entries but left the bindingless-container problem
human-dependent: old-binary leftovers (py-api: up 14h, zero port
bindings, unhealthy) stayed broken until a human clicked Restart.
Three more backlog entries closed:
1. **Monitor auto-heal** — on a failed probe the monitor delegates to
   `Server.HealApp`, which recreates the container from the shared
   `appSpec` when it has no published ports (same `ensureBinding` heal
   as `startApp`, extracted as a shared helper). Image apps recreate
   from `app.Image`; **git-source apps from the container's own
   worker-built image** (their `app.Image` is empty — the old "worker
   always binds them" exclusion was wrong for old-binary leftovers and
   is gone). 5-min `healCooldown` rate-limits retries; a heal records
   an `app_healed` event + `app auto-healed` alert. **E2e-verified
   live**: py-api healed itself ~5s after the new binary started, no
   human click — event id 26, alert delivered, probe 200.
2. **Health reset on start/restart/deploy** — handlers set
   `apps.health` `healthy` on success (monitor corrects within 90s if
   the app fails to serve); stop clears it so a stale "unhealthy"
   badge can't sit next to "stopped". Kills the ~60s stale-badge window
   after a healing restart. `healthReasonFor` copy now says "restart,
   or redeploy".
3. **No more swallowed store errors in the monitor** — `setHealth` /
   `recordEvent` log failures so the event trail can't silently desync
   from `apps.health`.
New unit tests: `TestHealApp` (image/git/no-image cases),
`TestAutoHeal*` in `internal/monitor/monitor_test.go` (first test file
for that package). `make test` + `make vet` green. Committed +
pushed this session (bjboss007 rule). The :8090 server runs this
binary.

**2026-08-29** — **App action feedback + status-badge bug fix** (browser-
verified). App start/stop/restart use HTMX to swap only `#head-actions`, so
the `?flash=` confirmation those handlers set never rendered — no page
reload. Fixes (`web/templates/apps.templ`, `web/static/app.css`): the
status badge now lives inside the swapped region so every action visibly
flips it; `hx-disabled-elt="find button"` + a CSS spinner give an in-flight
state (a stop can take ~10s). While verifying, found a **pre-existing
bug**: `statusBadge`/`healthBadge` used `@statusDot("running"){ status }`,
and templ read the `{ status }` as a literal children-text block — every
badge across the app showed the word "status" (and health showed
`"healthy"` with quotes) instead of the value. Fixed by passing the label
as a `statusDot` parameter. Verified live via curl against the running
server (minted+deleted a dev session): badge reads `running`; POST /stop
returns the partial with badge `stopped` + Start button; /start restores.
**A running server on :8090 is the freshly-built binary I restarted** (was
PID 74778).

**2026-08-29** — **Infra manifest teardown (surface-only)** shipped
(unit-tested, not yet e2e). A manifest-created service the manifest stops
declaring is flagged orphaned — badge on the service card, a build-log
line, and a `service_orphaned` event — but **never deleted** (the
manifest's "deleting infra is a human decision" rule). The human Keeps it
(adopts → origin `manual`, `POST /services/{slug}/keep`) or Deletes it.
Manual services are never flagged; dropping the whole manifest flags
nothing. Migration 0010 adds `services.origin` + `services.orphaned`;
`Provisioner.reconcileOrphans` does the diff. Spec + backlog updated.
Next manifest item: **seeding/backups** (overlaps Database backups).
**Not verified:** real deploy exercising the orphan path (unit tests
cover flag/clear/manual-immunity). **Not yet committed.**

**2026-08-29** — **Environments + image pins** shipped and e2e-verified
(infra-manifest v2, ADR 0017): apps and services carry
`staging`/`production` labels; per-env manifest overlays
(`deploymate.{env}.yml` replaces the base services list); service
resolution and URL injection are environment-scoped (`staging-postgres`
vs `postgres`, separate volumes); manifest entries accept pins
(`postgres:17`) and **no tag means `latest`** (owner's rule —
`postgres:latest` caveat: Postgres volumes are major-version-locked, so
pin in production). `POST /apps/{slug}/environment` + env badges in the
UI. E2e verified: both envs in one project with isolated services and
URLs, overlay replacement, latest + pinned images, idempotent redeploys.
Spec: `docs/specs/infra-manifest.md`. Also earlier the same day: infra
manifest v1 (commit `58ae4ec`, pushed) and the progress/CLAUDE.md
handoff files (`abcbbb2`).

**Commits:** the morning fix wave (`5d3ffec`, `e23291b`) and this
session's auto-heal wave were all pushed with the bjboss007 rule.

**Verified state:** everything shipped so far is e2e-verified on macOS/
Docker Desktop (auto-DNS was verified live on :8090 against the real
Cloudflare API). **NOT verified:** real Ubuntu server `bootstrap.sh` run,
real Let's Encrypt issuance.

## Next up (ordered)

1. **Real e2e suite (continue)** — git-deploy path is now automated
   (`make e2e-git`); still unautomated: env injection + manifest services,
   rollback, Traefik labels, metric/uptime assertions, the three Railpack
   runtimes. Next slices: extend `e2e_git.sh` to assert env injection via a
   manifest service, and add a rollback assertion. Longer-term: wire both
   e2e scripts into CI on a Linux runner (Traefik + railpack work there).
   Entry: `testdata/e2e_git.sh`, `testdata/e2e.sh`, `cmd/deploymate/seed.go`,
   the verification sections in `docs/specs/*.md`, backlog item in
   `docs/improvements.md` (Near-term).
2. **Preview DNS lifecycle / dedicated domain** — auto-DNS *creates*
   per-app CNAMEs, but deleting an app leaves its CNAME behind (add
   API-side cleanup on delete). And **preview https is blocked on the
   current hostnames**: free Universal SSL only covers one wildcard level
   (`*.getmerchanttech.com`; verified via cert SANs + edge alert 40 on
   `*.dm.…`), so `{slug}.dm.getmerchanttech.com` can never get edge certs.
   **Owner's decision (Aug 2026): register a dedicated DeployMate domain**;
   previews move to `{slug}.newdomain` (first level → wildcard cert covers
   them), switch tunnel ingress + `DEPLOYMATE_PREVIEW_HOST` +
   `DEPLOYMATE_CLOUDFLARE_ZONE_ID`, then flip `previewURL` to `https://`.
   Entries: `internal/dns`, `internal/httpserver/handlers_preview.go`,
   `docs/specs/cloudflare-tunnel.md`, backlog items (Near-term).
3. **Deployment command/timeout** — manual deploys run in the HTTP handler
   with no timeout; move them onto the worker queue. Entry:
   `internal/httpserver/handlers_apps.go` (`handleAppDeploy`),
   `internal/jobs/worker.go`, backlog item (Near-term).
4. **Recurring bindingless containers** — the auto-heal is reactive (on
   probe failure); the root cause (pre-fix binaries starting containers
   without bindings) is gone now that the fix binary is deployed, but if
   it recurs, investigate why starts produce unbound containers.
5. Anything else: the full ordered backlog is `docs/improvements.md`.

**Bigger milestone on the horizon:** first real-server run
(`deploy/bootstrap.sh` on Ubuntu 24.04, production LE certs) —
`docs/knowledge/server-setup.md`, `deploy/bootstrap.sh`.

## Before claiming something works

- `make test` and `make vet` must pass.
- A feature is "done" only when **e2e-verified** against a running server
  (manual verification is the norm until the real e2e suite exists). The
  throwaway pattern used for the manifest verification: fresh server on a
  spare port + scratch data dir + local git repos, clean up after.
- If you notice a gap while working: add it to `docs/improvements.md`
  **first**, fix it with its own change later.

## Reading path (pointers, not scans)

- Whole system on one page: `docs/knowledge/architecture.md`
- The heart of the product: `docs/knowledge/deploy-flow.md`
- Dev environment quirks (port 8090, Docker Desktop socket, SDK pins,
  templ path, railpack): `docs/knowledge/dev-environment.md`
- Decisions: `docs/decisions/` — 0001 first; 0013 Railpack, 0014 alerts,
  0015 resources/port mapping, 0016 env aliasing
- Specs: `docs/specs/` — alerts, cloudflare-tunnel, infra-manifest
- Database semantics: `docs/knowledge/database.md` ·
  security: `docs/knowledge/security.md` ·
  troubleshooting: `docs/knowledge/troubleshooting.md`
- Backlog: `docs/improvements.md`

## Hard-won rules (skipping these burns an hour)

- **Before every push:** `gh auth switch --user bjboss007`, then verify
  `gh auth status` shows it active — the account flips back to
  `habibmuhammad002` on its own, and pushes fail with "Repository not
  found". Remote: `github.com/bjboss007/deploymate` (HTTPS).
- **Manifest versions:** entries without a version run `latest` (owner's
  rule). Postgres volumes are major-version-locked — `postgres:latest`
  bumping majors can refuse to start on an old volume; pinning is the
  documented escape hatch.
- **templ literals:** templ treats `{...}` as expressions — literal
  braces in text need a string expression (`{ "deploymate.{env}.yml" }`),
  and `\{` is illegal. `make gen` regenerates; commit `_templ.go` too.
- **Git-source apps have no `app.Image`** — the row is empty; the
  container's own image (worker-built `deploymate/apps/{slug}:{hash}`,
  readable via `runtime.Info.Image`) is the ground truth for any
  recreate. Note: the docker SDK's `ContainerJSON.Image` can come back
  as a bare `sha256:` digest instead of the tag.
- **Subpath-hosted apps and URL resolution** — a `<base href>` tag does
  NOT fix absolute-path URLs (`/assets/x` replaces the base's path);
  only relative URLs (`./assets/x`) respect it. Relative URLs resolve
  against the DOCUMENT URL's directory — so a subpath host must
  canonicalize to a trailing slash (`/preview/{slug}/`), and apps
  should emit relative URLs (`vite base: "./"`, relative `fetch`) to
  work under any prefix. `fetch("/api/…")` inside JS bundles can't be
  rewritten by a proxy — app-side relative URLs are the only fix.
- **`.gitignore` `/deploymate` is root-anchored on purpose** — a bare
  `deploymate` also matches the `cmd/deploymate/` source dir and silently
  un-tracks the whole main package. Keep the leading slash. After any
  build, sanity-check `git ls-files cmd/` is non-empty.
- **`go build ./cmd/deploymate` writes `./deploymate`, not `bin/`** — use
  `make build` (outputs `bin/deploymate`); a stale `bin/` binary runs old
  code silently. The e2e scripts `make build` unless `DM_BIN` is set.
- Docs live in `docs/` — ADRs, knowledge, specs, improvements. Update them
  with the code, not later.
- Never silently fix a gap in scope — backlog it first (see above).

## Update protocol (before stopping, every session)

1. Rewrite "Where we stopped" (date, what shipped, commit, what's
   unverified) and re-order "Next up".
2. Add any new quirk/rule you discovered.
3. Commit `progress.md` together with the work and push (bjboss007 rule).
