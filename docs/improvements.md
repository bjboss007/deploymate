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
- [ ] **Preview hostnames go live** — routing, DNS records, and the
  dashboard UI are all in place; the remaining piece is Cloudflare's
  free-plan edge certs for the per-app hostnames (provisioning on first
  use, can take up to a day) and **auto-DNS**: new apps should get their
  CNAME automatically via the Cloudflare API instead of a manual
  `cloudflared tunnel route dns` per app. Also worth deciding: preview
  subdomains are public by design — a per-app private toggle + Cloudflare
  Access is the lock-down path.
- [ ] **Deployment command/timeout** — manual deploys run in the HTTP
  handler with no timeout; a hung pull blocks the request. Move manual
  deploys onto the worker queue (they already create deployment rows).
- [ ] **Container command override** — image deploys can't pass a
  command/args. Useful for one-off jobs and images with odd entrypoints.
- [ ] **Disk usage panel** — `docker system df` + per-volume sizes on the
  dashboard; `images.size_bytes` is recorded but never displayed or
  filled from the daemon. *Bitten us already: Docker Desktop's disk
  filled, MySQL init started failing ("UUID failed", ENOSPC) and it took
  an hour to diagnose (Aug 2026) — at minimum surface a disk warning.*
- [ ] **`build_logs` retention** — build logs accumulate forever; prune
  with the same hourly pass as metrics (e.g. keep 30 d).
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
- [ ] **Real e2e suite** — `testdata/e2e.sh` covers the P2 smoke path
  only. Manual verification exists for P3–P6 (env injection, webhook→
  deploy→rollback, Traefik labels, metric/uptime assertions, three
  runtimes) but none of it is automated; wire into CI with a Linux
  runner (Traefik works there).

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
- [ ] **App log history** — logs are live-only; add a small ring buffer
  per app (or `docker logs` snapshot) so the panel shows context before
  the stream connects.
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
- Blue/green or canary deploys (weighted Traefik routers)
- Scheduled/cron deploys and jobs
- Per-app build cache settings (`--cache-from` registry or local dir)
- Dark-mode polish pass on charts + log panel (the theme system exists)
