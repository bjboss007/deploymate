# Improvements

The backlog. Priorities are scoped to one maintainer. Rule: **if you notice
a gap while working, add it here first**, fix it later with its own
change.

## Near-term (high value, low risk)

- [ ] **Reap stale `building` deployments** — a hard kill mid-build leaves
  rows stuck forever (no queue progress, app shows "building"). On worker
  startup, fail any `building` rows whose owner is gone, or add a
  heartbeat + timeout.
- [ ] **Deployment command/timeout** — manual deploys run in the HTTP
  handler with no timeout; a hung pull blocks the request. Move manual
  deploys onto the worker queue (they already create deployment rows).
- [ ] **App healthchecks** — `runtime.Spec` has no healthcheck field;
  containers run without one. Add optional healthcheck (path/port) so
  "running" means healthy, and surface `unhealthy` state in the UI.
- [ ] **Container command override** — image deploys can't pass a
  command/args. Useful for one-off jobs and images with odd entrypoints.
- [ ] **Disk usage panel** — `docker system df` + per-volume sizes on the
  dashboard; `images.size_bytes` is recorded but never displayed or
  filled from the daemon.
- [ ] **`build_logs` retention** — build logs accumulate forever; prune
  with the same hourly pass as metrics (e.g. keep 30 d).
- [ ] **TLS status sync** — `domains.tls_status` stays `pending` forever;
  parse Traefik's acme.json (or enable Traefik's API on localhost) to
  show `active`/`failed` + expiry. Uptime probes already reveal the real
  state indirectly.
- [ ] **Real e2e suite** — `testdata/e2e.sh` covers the P2 smoke path
  only; extend per phase (P3 env injection, P4 webhook→deploy→rollback,
  P5 label assertions, P6 metric/uptime assertions) and wire into CI
  with a Linux runner (Traefik works there).

## Medium-term (feature depth)

- [ ] **Nixpacks/Railpack fallback builds** — repos without a Dockerfile
  currently fail with a clear message. Railpack (Go, BuildKit LLB) is the
  modern choice; Nixpacks is maintenance-mode.
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

- Resource limits per app (CPU/memory) in the UI
- Container runbook: view env/effective config of a running app
- Import/export: migrate apps between DeployMate instances
- Blue/green or canary deploys (weighted Traefik routers)
- Scheduled/cron deploys and jobs
- Per-app build cache settings (`--cache-from` registry or local dir)
- Dark-mode polish pass on charts + log panel (the theme system exists)
