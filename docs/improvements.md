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
- [ ] **Preview hostnames** — the server-side sibling of preview URLs:
  `{slug}.{server-ip}.nip.io`-style auto-subdomains via Traefik with
  staging TLS, so previews work without a real domain AND without the
  dashboard in the URL path. Requires Traefik, so server-only (see
  `deploy/traefik/`).
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
- [ ] **Env var aliasing / injection mapping** — today each service type
  injects one fixed key (DATABASE_URL/MYSQL_URL/REDIS_URL). VGG needed a
  MySQL URL under the key `DATABASE_URL` — we copied the connection
  string manually. Let apps map any injected URL to any env key.
- [ ] **Database backups** — `pg_dump`/`mysqldump`/redis SAVE on a
  schedule into the data dir (or S3), with restore UI. Named volumes
  alone are not backups.
- [ ] **App log history** — logs are live-only; add a small ring buffer
  per app (or `docker logs` snapshot) so the panel shows context before
  the stream connects.
- [ ] **Deploy previews / per-branch deploys** — deploy a PR branch to a
  preview domain, cleaned up on merge. Requires multiple domains per app
  + per-deployment routing labels.
- [ ] **Automatic deploy on git connect** — after linking a repo, offer
  "deploy now" in the same flow (today it's two clicks).
- [ ] **Notifications** — deploy success/failure to a webhook (Slack,
  Telegram, email). Natural fit: the worker already publishes events to
  the broker.
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
