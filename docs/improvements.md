# Improvements

The backlog. Priorities are scoped to one maintainer. Rule: **if you notice
a gap while working, add it here first**, fix it later with its own
change.

## Near-term (high value, low risk)

- [x] **Reap stale `building` deployments** — done: the worker fails
  in-flight rows on startup (Aug 2026).
- [x] **Preview URLs** — done: `/preview/{slug}` on the dashboard
  reverse-proxies to the app via loopback-published ports, absolute
  app redirects rewritten into the preview path (Aug 2026).
- [x] **Runtime environments via Railpack** — done: 11 runtimes with
  version pinning, no Dockerfile needed; node:22, python:3.12, java:21
  verified end-to-end (Aug 2026).
- [x] **Alerts & notifications** — done: webhook targets with per-event
  subscriptions, delivery history in the UI, events for deploys,
  health, uptime transitions, restarts, and docker storage growth
  (spec: docs/specs/alerts.md, ADR 0014; Aug 2026).
- [x] **App healthchecks** — done: monitor probes every running app on
  its loopback port (3-fail/2-ok hysteresis), `apps.health` column,
  badges on cards + app pages, `app_unhealthy`/`app_recovered` alerts
  (Aug 2026). The VGG crash-loop incident is now a notification instead
  of a silent "running".
- [x] **Event history & insights** — done: append-only events table
  (health transitions, resource updates/resizes, start/stops, env and
  runtime changes) + per-app History page with derived stats — deploys,
  success rate, avg build time, uptime %, MTTR, incidents (Aug 2026).
- [x] **One-click Restart** — done: apps (hx swap) and services
  (stop→start→readiness in one POST), events recorded (Aug 2026).
- [x] **Stable Cloudflare tunnel** — done: named tunnel on
  getmerchanttech.com with `DEPLOYMATE_PREVIEW_HOST` hostname routing
  and per-app DNS records; webhook now on a stable URL
  (spec: docs/specs/cloudflare-tunnel.md, Aug 2026).
- [x] **Infra manifest (`deploymate.yml`)** — done: a repo declares the
  services it needs; every git deploy reconciles the project's services
  with the declaration (reuse a running one, start a stopped one,
  auto-provision a missing one), then injects the URLs. Never deletes;
  failures fail the deploy. Provisioning extracted into a shared
  `ServiceProvisioner` used by handlers and the worker (spec:
  docs/specs/infra-manifest.md; Aug 2026).
- [x] **Environments + image pins (infra manifest v2)** — done:
  `apps.environment`/`services.environment` labels (staging |
  production), per-env manifest overlays (`deploymate.{env}.yml`
  replaces the base services list), env-scoped service resolution and
  URL injection (`staging-postgres` vs `postgres`), and pinned manifest
  entries (`postgres:17`; no tag → `latest`) — the service row's image
  is now what runs. ADR 0017; verified e2e (Aug 2026).
- [x] **Auto-DNS for preview hostnames** — new apps get their preview
  CNAME created automatically via the Cloudflare API (`internal/dns`,
  `DEPLOYMATE_CLOUDFLARE_*` env). Best-effort: failures never block app
  creation and surface as a `dns_record_failed` event + warning flash
  (Aug 2026).
- [x] **`dev` environment (infra manifest v2.1)** — done: `dev` is a
  third app/service environment alongside staging/production and the
  **default for newly-created apps** (was production). Behaves exactly
  like staging — env-prefixed services (`dev-postgres` / "Dev
  PostgreSQL", volume `dm-svc-dev-postgres-data`), a `deploymate.dev.yml`
  overlay, full isolation. `EnvDev` constant + `CreateApp` default flip;
  `handleAppEnvironment` accepts it; the provisioner's service label
  generalized from a hardcoded "Staging " prefix to a capitalized env
  name; UI selector option + blue `.badge-dev` tint. No migration (the
  `environment` column has no CHECK constraint). ADR 0017 addendum;
  verified e2e (commit `a48665c`, Aug 2026).
- [ ] **Preview https** — `previewURL` (`internal/httpserver/
  handlers_preview.go`) still emits `http://` because edge cert issuance
  lags app creation (first issuance can take minutes to hours); flip to
  `https://` once issuance is confirmed reliable. **Decision (Aug 2026):
  move previews to a dedicated DeployMate domain** — free Universal SSL
  covers `*.domain` only one level deep, so the current
  `{slug}.dm.getmerchanttech.com` hostnames (two levels) can never get
  edge certs on the free plan (verified: cert SANs are
  `getmerchanttech.com` + `*.getmerchanttech.com` only; edge returns
  alert 40 for `*.dm.…`). Once the dedicated domain is registered:
  previews live at `{slug}.newdomain` (first level → wildcard cert
  covers them), tunnel ingress + `DEPLOYMATE_PREVIEW_HOST` +
  `DEPLOYMATE_CLOUDFLARE_ZONE_ID` switch over, then flip `previewURL`
  to `https://`.
- [x] **Preview DNS cleanup** — done: deleting an app removes its preview
  CNAME via the Cloudflare API (list-by-name then delete; no match or a
  404 racing the delete is success). Best-effort like creation: deletion
  never blocks, a failure just flashes a warning on the project redirect
  (Sep 2026).
- [ ] **Preview privacy** — preview subdomains are public by design; a
  per-app private toggle + Cloudflare Access is the lock-down path.
- [x] **Deployment command/timeout** — done: the Deploy button queues a
  `manual` deployment row (kind/trigger `manual`, `queued`) and redirects
  to `/deployments/{id}`; the worker's `runManualDeploy` pulls (bounded at
  10 min — a hung registry fails the deploy instead of blocking the HTTP
  request), then runs the shared zero-downtime swap + `finish`/`fail`.
  Progress/failures land on the deployment page like git deploys; manual
  rows now get `started_at`/`finished_at` (durations on the releases page
  were empty before). E2e-verified on a throwaway server (Sep 2026).
- [ ] **Server-side dashboard routing (Traefik file provider)** — bootstrap
  installs Traefik with the docker provider only, but the dashboard runs
  as a host systemd service on `127.0.0.1:8080` (not a container), so on a
  real server nothing routes `dm.example.com` or preview subdomains to it
  — in dev this was always cloudflared → localhost. Add a file-provider
  router (dashboard hostname + preview wildcard → `http://127.0.0.1:8080`)
  with **explicit router priority** so the preview catch-all can't swallow
  app-domain routers (Traefik's default priority = rule length, which the
  `HostRegexp` catch-all would win — exact `Host()` rules on apps would
  lose). **Blocker for the first real-server run (shape A)**. Found Aug
  2026 during deployment planning.
- [x] **Deploy-time diff review** — done: the Git panel's one-click deploy
  is now a two-step flow. `GET /apps/{slug}/deploy-preview` shows commits
  + file counts (+/-) vs the currently deployed commit (persistent full
  mirror clones in `data/repos/mirror-{sourceID}` — `internal/gitpkg/
  mirror.go`: `MirrorSync`/`MirrorEnsureSHA`/`MirrorRange`, `ErrCommitGone`
  when the deployed commit was force-pushed away), and the confirm form
  **pins the reviewed SHA** (`sha` form field — `gitpkg.Clone` already
  honored pinned SHAs). Webhook deploys skip the review but get the same
  range recorded in the build log (`Worker.logDiffRecord`). First-deploy
  and nothing-to-deploy states. E2e-verified: first deploy → second
  commit → review shows the new range → pinned deploy (Aug 2026).
- [x] **Fleet-wide build statistics** — done: `GET /stats` with 30 d
  totals, success rate, avg build time, a per-app table, and a deploys-per-
  day chart (store: `DeploymentStatsAll`/`DeploymentStatsPerApp`/
  `DeploysPerDay`; `stats.templ` + topbar link). Also fixed: manual image
  deploys (kind `manual`) were excluded from `DeploymentStatsFor` (History
  page) — now counted fleet-wide and per-app. E2e-verified (Aug 2026).
- [x] **Per-app releases list** — done: `GET /apps/{slug}/releases`
  (handlers_releases.go + releases.templ, "All releases →" on the app
  page): every deployment with status badge, commit/image, kind, trigger,
  duration, a "current" marker, `?status=all|successful|failed` filters,
  and one-click rollback. New migration 0011 adds `deployments.trigger`
  (dashboard | webhook | manual | rollback | resize; '' = legacy → UI
  falls back to kind) set at all five create call sites. Also fixed:
  manual deploys never set `apps.current_deployment_id` (only the worker
  did) — every manual row showed rollbackable and nothing was "current";
  `handleAppDeploy` now mirrors the worker's `finish`. E2e-verified (Aug
  2026).
- [x] **Container command override** — done: the image deploy form takes
  optional Entrypoint + Command fields (whitespace-separated, no quoting;
  empty = image default). Persisted on `apps.entrypoint`/`apps.command`
  (migration 0013) and applied by the shared spec chain — `runtime.Spec`
  + docker `container.Config` len-guards (the single "empty = not set"
  boundary), `appspec.SplitArgs` — so manual deploys, rollbacks, restarts,
  and binding heals all inherit them from the row. Git apps are
  unaffected. E2e-verified: busybox `httpd -f -p 8080` override served
  and cleared back to image defaults (Sep 2026).
- [x] **Disk usage panel** — done: a Storage panel on /stats with a live
  `docker system df` snapshot — stat cards per category (images,
  containers, volumes, build cache, reclaimable, tracked app images),
  every named volume with its size (volumes not owned by a DeployMate
  service flagged "not tracked"), and the 5 largest images. New
  `runtime.DiskUsage`/`ImageSize` methods (docker-free structs);
  `images.size_bytes` is now filled from a best-effort inspect at build
  time and summed via `Store.TotalImageBytes`. Best-effort by design: a
  nil runtime hides the panel, a daemon error renders a warning note —
  /stats never 500s on docker. E2e-verified (Sep 2026).
- [x] **`build_logs` retention** — done: `Store.PruneBuildLogsBefore`
  deletes lines older than 30 d, called from the monitor's hourly
  `prune()` pass alongside metrics/uptime (`buildLogsRetain` in
  `internal/monitor/monitor.go`). Deployment rows stay; only their verbose
  line-by-line output is pruned. Unit-tested (Aug 2026).
- [ ] **TLS status sync** — `domains.tls_status` stays `pending` forever;
  parse Traefik's acme.json (or enable Traefik's API on localhost) to
  show `active`/`failed` + expiry. Uptime probes already reveal the real
  state indirectly.
- [x] **Unhealthy badge mislabels running apps** — done: the app page
  now distinguishes crash loops (restarts > 0) from "container is
  running but failing health probes on its preview port" (0 restarts);
  `healthReasonFor` in `handlers_apps.go` is unit-tested. Seen on
  db-probe/py-api/web-front: up for hours, 0 restarts, probe unreachable
  (Aug 2026).
- [x] **Start/Restart can't heal a missing port binding** — done:
  `startApp` inspects after starting and, when an image app's container
  has no published ports, recreates it from the current spec (shared
  `appSpec` builder) so the probe becomes reachable. Git-source
  containers are excluded (the worker always binds them). Unit-tested +
  e2e-verified: a bindingless container healed by one restart click
  (Aug 2026).
- [x] **Deploy handler clobbers the port on empty form field** — done:
  `deployPort` falls back to the stored `app.Port` when the form field
  is empty, so a port-less deploy persists the port and keeps the
  preview binding. Unit-tested + e2e-verified (portless deploy kept
  port 8080 and bound the preview port) (Aug 2026).
- [x] **Stale bindingless containers need a human restart — no auto-heal**
  — done: the monitor auto-heals. On a failed probe it delegates to
  `Server.HealApp` (same `ensureBinding` heal as `startApp`), which
  recreates the container from the shared `appSpec` when it has no
  published ports — image apps from `app.Image`, git-source apps from
  the container's own worker-built image (their `app.Image` is empty;
  the old "worker always binds them" exclusion left old-binary leftovers
  like py-api permanently broken). 5-min `healCooldown` rate-limits
  retries; a heal records an `app_healed` event + `app auto-healed`
  alert. `healthReasonFor` copy now says "restart, or redeploy".
  Unit-tested + e2e-verified live: py-api (unbound, up 14h) healed
  itself ~5s after the new binary started, no human click (Aug 2026).
- [x] **Restart/start doesn't reset `apps.health`** — done: start,
  restart, and deploy handlers set health `healthy` after success (the
  monitor corrects within 90s if the app actually fails to serve);
  stop clears it so a stale "unhealthy" badge can't sit next to
  "stopped". Unit-tested (Aug 2026).
- [x] **Health event trail has gaps** — done (partially): the monitor no
  longer swallows store errors — `setHealth`/`recordEvent` log
  failures, so a desync between the event trail and `apps.health` is
  visible in the server log. The in-memory `healthFails` map is still
  lost on server restart (a stale `unhealthy` corrects itself within 2
  OK probes, so it's benign); the remaining audit waits for the real
  e2e suite (Aug 2026).
- [x] **Worker deploys don't reset `apps.health`** — done: the webhook/
  worker path (`finish` in `internal/jobs/worker.go`) left health empty
  until the monitor's next probe (~30s), while the dashboard deploy set
  it immediately. `finish` now sets `healthy` on success, matching the
  handlers (Aug 2026).
- [x] **Frontend frameworks deployable?** — proven: a React 18 + Vite 6
  SPA (vite build + node http server reading `$PORT`, `.mise.toml`
  node 22) deployed end-to-end via the git pipeline: Railpack detected
  node, ran `npm run build`, started `node server.js` on `$PORT`; SPA +
  `/api/ping` verified over the preview port, health green. Pure-static
  SPAs without a server still need a Dockerfile or a serving start
  command; per-runtime `--build-cmd`/`--start-cmd` overrides remain
  open as a Medium-term item (Aug 2026).
- [x] **SPAs render blank under the dashboard preview** — done (two
  rounds; the first fix was wrong). Round 1: a `<base>` tag injection —
  useless, because absolute-path URLs (`/assets/…`) *replace* the base's
  path; a real-browser reproduction proved the script still 404'd.
  Round 2 (the actual fix): the proxy rewrites `src="/…"`/`href="/…"`
  values in proxied text/html to carry the `/preview/{slug}/` prefix
  (protocol-relative and already-prefixed URLs untouched), and
  `handlePreview` 307-canonicalizes `/preview/{slug}` →
  `/preview/{slug}/` so relative asset URLs resolve inside the app's own
  directory. Apps should additionally emit relative URLs (`base: "./"`
  in vite; relative `fetch("api/ping")`) — then they work standalone
  under any subpath. Unit-tested (`TestPreviewProxyRewritesURLs`, real
  backend on the deterministic preview port) and verified in a real
  headless Chrome: login → `/preview/react-spa/` renders the React app,
  body text + `api/ping` → pong (Aug 2026).
- [ ] **Multi-webhook fan-out deduped to one app** — found live
  (2026-09-04) by the three-environment dogfood demo (one GitHub repo →
  one webhook per app, all on the same repo). GitHub sends every webhook
  on a repo the **same `X-GitHub-Delivery` GUID** for one push event
  (verified across three hook deliveries: identical `bb45e8b8-…`), so
  `DeliveryCache.Seen(provider, deliveryID)`
  (`internal/webhooks/webhooks.go`) — designed to dedupe retries on a
  *single* source — eats the 2nd..Nth fan-out deliveries: only the first
  hook to arrive queues a deployment, the rest get `200 "duplicate
  delivery ignored"` (response `Content-Length: 26` in GitHub's
  delivery records). Manual replays with a fresh GUID always queue, so
  the handler is otherwise fine. Fix idea: include the git source id in
  the dedupe key (`provider:sourceID:deliveryID`), or hash the payload
  instead of the GUID. **No e2e caught it** — `e2e_git.sh` fires one
  webhook; add a two-apps-one-repo assertion.
- [ ] **SSH deploy-key clones fail when `DEPLOYMATE_DATA_DIR` is
  relative or space-containing** — found live (2026-09-04, same demo).
  The worker's checkout + identity paths come from `cfg.DataDir`
  (`internal/jobs/worker.go` → `gitpkg.Clone`, key written at
  `checkoutDir+".key"`, `GIT_SSH_COMMAND="ssh -i <key> …"`). Two
  independent breakages on macOS/git 2.40.1, both reproducible outside
  the worker:
  1. **Relative `-i` path**: `git clone` with `-i data/repos/x.key` →
     ssh: "Identity file data/repos/x.key not accessible" even though
     the file exists (direct `ssh -i rel.key` works; absolute `-i`
     through git works). The live server launched with `DataDir=./data`
     → every SSH-URL deploy died at clone with "Repository not found".
  2. **Unquoted absolute path with spaces**: `-i /Users/…/Start
     Up/…/key` splits on the space → ssh resolves a bogus hostname.
  Workaround (used live): run the server with an **absolute,
  space-free** data dir (`~/dm-data` → symlink). `e2e_git.sh` never
  exercised SSH (local-path repos) — a real-SSH clone e2e on an
  absolute scratch dir would have caught this. Fix idea: build
  `keyPath` as an absolute path and shell-quote it in the ssh command.
- [ ] **Real e2e suite** — `testdata/e2e.sh` covers the P2 image smoke
  path; `testdata/e2e_git.sh` (`make e2e-git`, Aug 2026) now covers the
  **git-deploy path** end to end on a throwaway server (signed webhook →
  worker clone/build/swap → preview-proxy probe), via the new
  `seed-git-source` subcommand + `testdata/repos/e2e-web` fixture. Still
  unautomated: env injection + manifest services, rollback, Traefik
  labels, metric/uptime assertions, the three Railpack runtimes (the git
  suite uses the Dockerfile engine to avoid a buildkit dependency). Wire
  the lot into CI with a Linux runner (Traefik + railpack work there).
- [ ] **Per-source branch not editable after connect** — `default_branch`
  is set at connect/seed time only; the demo (Sep 2026) re-pointed an app
  at another branch (dev → `develop`, staging → `stage`) with a direct
  sqlite UPDATE of `git_sources.default_branch`. The handler already
  differentiates webhooks by it (payload `ref` vs the source's
  DefaultBranch, checked before the delivery dedupe — so one-repo
  multi-app branch-per-environment flows work and dodge the fan-out
  dedupe bug entirely); only the *editing* is missing. Add a branch field
  to the app page + an update endpoint.

## Medium-term (feature depth)

- [ ] **Railpack `--cache-to/--cache-from`** — wire build cache export
  (BuildKit registry cache) so rebuilds across deploys are faster than
  cold.
- [ ] **Per-runtime build/start command overrides** — Railpack supports
  `--build-cmd`/`--start-cmd`; surface them in the Build panel for
  runtimes whose auto-detection falls short. *The VGG deploy needed its
  heroku profile + a custom DATABASE_URL — solved via env vars, but a
  start-cmd override is the cleaner general tool.*
- [x] **Env var aliasing** — done: env values resolve `${KEY}` references
  against the full computed env, chained with a cycle guard (ADR 0016).
  VGG's DATABASE_URL is now just `${MYSQL_URL}` — no copied credentials
  (Aug 2026).
- [ ] **Database backups** — `pg_dump`/`mysqldump`/redis SAVE on a
  schedule into the data dir (or S3), with restore UI. Named volumes
  alone are not backups.
- [x] **Infra manifest teardown (surface-only)** — done: a
  manifest-created service the manifest stops declaring is flagged
  orphaned (badge + build-log line + `service_orphaned` event), never
  deleted. The human Keeps it (adopts → origin `manual`) or Deletes it.
  Manual services are never flagged; dropping the whole manifest flags
  nothing. Migration 0010 (`services.origin`, `services.orphaned`);
  `reconcileOrphans` in the provisioner; `POST /services/{slug}/keep`
  (Aug 2026). Auto-delete stays deliberately out of scope.
- [ ] **Infra manifest seeding** — the remaining manifest extension from
  docs/specs/infra-manifest.md: database seeding/backups declared from
  the manifest. Overlaps with **Database backups** above. Pins,
  environment-specific manifests, and teardown-surfacing shipped.
- [x] **App log history** — done (stateless snapshot; the DB ring buffer
  was deliberately judged overkill for one maintainer): when the log panel
  opens on a **stopped but not-removed** container, `handleAppLogs` now
  emits a non-follow `docker logs` tail (last 200 lines) so you see *why*
  the app went down instead of a blank "waiting". The running path already
  tails 200 for context. Demux extracted into a shared `writeContainerLogs`
  helper (unit-tested). Limitation by design: a **removed** container
  (between deploys) has no docker logs, so pre-recreate history is not
  shown — persisting across recreation would need the DB ring (see below).
  Unit-tested (Aug 2026).
- [ ] **App log persistence across recreation** — the stateless snapshot
  above cannot show a container's logs once it's removed (every deploy/heal
  recreates). If pre-deploy runtime output becomes worth keeping, add a
  DB-backed per-app ring: the monitor snapshots each running app's new
  lines (deduped by docker's per-line timestamp) into an `app_logs` table,
  capped per app by the hourly prune pass; the panel loads it on connect,
  then follows live. Deferred as overkill for now.
- [ ] **Deploy previews / per-branch deploys** — deploy a PR branch to a
  preview domain, cleaned up on merge. Requires multiple domains per app
  + per-deployment routing labels.
- [ ] **Multi-environment app instances** — the bigger sibling of the
  environment labels (ADR 0017 deferred it deliberately): one app row
  running staging AND production containers simultaneously, each with
  its own domains (`api-staging.example.com` vs `api.example.com`),
  env vars, deploy targets, and rollback history. The 0017 columns and
  naming (`staging-{type}` services) were designed to compose with it.
  Sub-items: per-env domains, per-env preview hostnames
  (`{slug}-staging.{previewHost}` — the preview middleware already
  routes hyphenated subdomains), per-env env-var sets.
- [ ] **Manual service environment selector** — hand-created services
  are production-only by design (the manifest is the staging path); if
  that bites, add an environment dropdown to the service create form
  (small: the column and filtering already exist).
- [ ] **Automatic deploy on git connect** — after linking a repo, offer
  "deploy now" in the same flow (today it's two clicks).
- [x] **Zero-downtime deploys** — done (blue/green-lite). A deploy starts
  the new container BESIDE the old (`dm-{slug}-{deployID}`, temp loopback
  host port, own Traefik router + UnixNano priority), probes it like the
  monitor does, then flips `apps.preview_host_port` (new migration 0012;
  proxy + monitor resolve through it with the per-slug hash fallback),
  removes the old container, and renames the staged one to the canonical
  name — all in `internal/swap` (probe-fail → staged removed, old keeps
  serving, app stays running). Shared `internal/appspec` replaces the two
  parallel spec builders (fixes the stale-`app.Port` Traefik drift);
  `runtime.Rename` added; worker `fail()` + deploy handler only mark the
  app failed when nothing runs for it. Rollback/resize reuse the swap.
  E2e-verified: 33/33 preview probes 200 across a live deploy; a broken
  image leaves the old container serving (Aug 2026).
- [x] **Notifications** — done via the alerts system: webhook channel
  with per-event subscriptions (Slack-compatible). Email/Telegram remain
  as additive channels (the `channel` column is the seam, Aug 2026).
- [ ] **Webhook secret rotation UI** — regenerating a webhook secret
  required a DB hack this session (the newline bug made it worse). A
  "rotate secret" button on the Git panel is the proper fix.
- [ ] **Prometheus `/metrics` endpoint** — additive to the SQLite
  sampling; enables Grafana if it's ever wanted.
- [ ] **Key rotation** — `v1:` envelope versioning exists precisely for
  this: a `v2` + re-encrypt loop (`internal/crypto`).

## Later (platform growth)

- [ ] **Remote servers** — implement `runtime.Runtime` over SSH/gRPC to a
  remote docker daemon; add a `servers` table (the FKs are shaped for
  it). This is the big one; the interface seam is the whole preparation.
- [ ] **Multi-user / teams** — schema has `user_id`/`role` everywhere;
  add invitation flows, per-user API tokens, and permission checks (a
  policy layer over handlers).
- [ ] **Traefik hardening** — put `tecnativa/docker-socket-proxy`
  between Traefik and the socket so the proxy only sees the API verbs it
  needs.
- [ ] **Self-update** — `deploymate update` (download + replace binary +
  restart via systemd) plus a version endpoint in the dashboard.
- [ ] **Postgres metadata migration** — if/when multi-server or
  multi-user pushes SQLite, goose SQL is portable; the work is in
  connection handling (drop `SetMaxOpenConns(1)`, add a pool).

## Ideas (unscoped)

- [x] **Resource limits per app** — done, automatically: limits are
  derived from each app's own P90 usage (24 h, 2× headroom, clamped) and
  applied as docker limits at the next deploy; port maps to the `PORT`
  env with an 8080 default (ADR 0015, Aug 2026).
- [x] **Auto-resize under pressure** — done: sustained usage past 80%
  of the applied limit triggers a limit bump + no-build redeploy before
  the OOM, with a DB-backed 30-min cooldown and `resource_resized`
  alerts (ADR 0015, Aug 2026).
- Container runbook: view env/effective config of a running app
- Import/export: migrate apps between DeployMate instances
- Blue/green or canary deploys (weighted Traefik routers) — the
  zero-downtime deploy item (Medium-term) is this family's immediate,
  simpler sibling; true canary weighting stays here
- Scheduled/cron deploys and jobs
- Per-app build cache settings (`--cache-from` registry or local dir)
- Dark-mode polish pass on charts + log panel (the theme system exists)
