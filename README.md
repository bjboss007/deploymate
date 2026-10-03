# DeployMate

A self-hosted PaaS for your own baremetal server — a personal Vercel/Railway.
One Go binary, one server, Docker as the compute substrate.

```
        GitHub (webhook, HMAC)                    browser
              │                                    │ https://app.example.com
              ▼                                    ▼
┌───────────────────── Ubuntu host ────────────────────────────────┐
│  deploymate (systemd Go binary, :8080 on 127.0.0.1)              │
│   dashboard/API · deployment worker · runtime · monitor · SQLite │
│                    │ /var/run/docker.sock                         │
│   ┌── Docker Engine ──────────────────────────────────────────┐  │
│   │  deploymate-net: app-1  app-2  postgres-1  redis-1        │  │
│   │  traefik (80/443 — the ONLY public ports)                 │  │
│   └───────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────┘
```

## Features

- **Projects** grouping apps, databases, and caches
- **Apps** — deploy a container image in seconds, or connect a git repo
- **Git deploys** — deploy key + webhook (GitHub/GitLab/Gitea), every push to
  your branch builds and deploys automatically, with live build logs and
  one-click rollback (last 5 images kept). Dashboard deploys go through a
  **review page** (commits + changed files since the live commit) and pin
  the reviewed SHA. Build from a **Dockerfile** or
  select a **runtime** — Node.js, Python, Go, Ruby, PHP, Java, Rust, Deno,
  Elixir, .NET, static sites — built with Railpack, no Dockerfile needed,
  optionally version-pinned
- **Fleet board** — the home page answers "is anything broken?": one status
  line, a *Needs attention* list with the real failure reason, and a row per app
  with a 24-hour heartbeat strip (up/down/stopped per hour, notches for
  deploys). Dark by default, light theme one click away.
- **Prebuilt deploys (GitHub Actions)** — for JVM apps too big to build on a
  small server: a workflow in your repo builds the JAR, DeployMate downloads
  that one artifact (fine-grained, read-only token, one repo) and runs it in a
  non-root `eclipse-temurin` wrapper with the same zero-downtime swap and
  rollback. Switch per app from the Git panel (the default stays "build on
  this server"): a Test connection check (with a warning if the token is
  wider than one repo), a ready-to-copy workflow file, and a **Deploy latest
  successful run** button for the first deploy or a missed webhook.
- **Infra manifest** — a `deploymate.yml` in the repo declares the backing
  services (`services: [postgres, redis]`); every deploy reconciles the
  project's services with it — reuse a running one, start a stopped one, or
  auto-provision a new one — then injects the connection URLs. Entries can
  pin images (`postgres:17`; no tag means `latest`). Services are never
  deleted for you.
- **Environments** — every app runs in `dev`, `staging`, or `production`.
  Each environment resolves its own services (`dev-postgres`,
  `staging-postgres`, `postgres` — separate data volumes) and can carry a
  `deploymate.{env}.yml` overlay that replaces the base service list for
  that environment.
- **Zero-downtime deploys** — the new container starts beside the old one
  and only takes over after passing a readiness probe; a failed deploy
  leaves the previous version serving
- **Databases & caches** — one-click Postgres 16, MySQL 8, Redis 7 with
  generated passwords (encrypted at rest), named volumes, readiness checks,
  and automatic connection-string injection (`DATABASE_URL`, `MYSQL_URL`,
  `REDIS_URL`) into every app in the project
- **Database backups** — opt-in per Postgres service: scheduled (cron)
  `pg_dump` snapshots, gzip + encrypted with a per-service key, uploaded to
  any S3-compatible bucket (Cloudflare R2) with keep-newest-N retention,
  "Back up now", and typed-confirm restore from the service page
- **Environment variables** — per-app, encrypted at rest, secrets masked in
  the UI; values can reference injected URLs (`DATABASE_URL=${MYSQL_URL}`).
  Add several at once: **+ Add variable** appends rows, one **Save** stores
  them all (or paste a `.env` file); all-or-nothing, and names like
  `*_PASSWORD`/`*_SECRET`/`*_TOKEN` are masked automatically
- **Automatic resource limits** — CPU/memory limits are derived from each
  app's own usage (P90 over 24h, doubled for headroom), and an app that
  sustains 80% of its limit is resized and redeployed automatically
- **Domains & HTTPS** — automatic Let's Encrypt via Traefik (staging
  resolver by default), label-driven routing with zero proxy restarts
- **Replicas** — run 1–5 identical containers per app behind one address:
  load-balanced with unhealthy replicas skipped, rolling deploys that keep
  at least one replica serving, scale up/down without a rebuild, merged
  logs and per-replica metrics, and a health path checked on save
- **Preview URLs** — every running app gets `/preview/<slug>` on the
  dashboard instantly, before any domain exists; with a preview host and
  a Cloudflare tunnel, a public `{slug}.{host}` subdomain whose DNS record
  is created and removed with the app
- **Healthchecks & auto-heal** — every running replica is probed every
  30s; unhealthy state shows on cards and app pages (with the reason), and
  containers that stop or lose their port binding are restarted/recreated
- **Alerts** — webhook notifications (Slack-compatible) for deploy
  failures/successes, unhealthy/recovered apps, uptime transitions,
  container restarts, and docker storage growth, with delivery history
  in the dashboard
- **Monitoring** — CPU/memory charts (5s sampling), live container logs over
  SSE (last output kept for stopped containers), and 30s uptime probes per
  domain with history
- **Releases, history & stats** — per-app releases list with filters and
  one-click rollback, an event timeline, and a fleet `/stats` page (30-day
  build success/duration, deploys per day, docker disk usage incl.
  untracked volumes)

## Run locally (dev)

```sh
make build
DEPLOYMATE_SETUP_EMAIL=you@example.com DEPLOYMATE_SETUP_PASSWORD=secret \
  ./bin/deploymate setup-admin   # prompts for anything not set
make dev                         # http://127.0.0.1:8090 (Docker Desktop)
```

Or one-shot bootstrap:

```sh
make build
DEPLOYMATE_DATA_DIR=./data DEPLOYMATE_SETUP_EMAIL=you@example.com \
DEPLOYMATE_SETUP_PASSWORD=secret ./bin/deploymate serve
```

Open http://127.0.0.1:8090 and sign in. Port 8090 locally because Docker
Desktop occupies 8080; on the server the default is 8080.

## Deploy to your server (Ubuntu 24.04)

```sh
# on your Mac / build machine
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o deploymate-linux ./cmd/deploymate
scp deploymate-linux root@your-server:/tmp/

# on the server
DEPLOYMATE_LE_EMAIL=you@example.com bash deploy/bootstrap.sh /tmp/deploymate-linux
sudo -u deploymate /usr/local/bin/deploymate setup-admin
```

Then: point an A record at the server, attach a domain to an app, set
`DEPLOYMATE_LE_MODE=production` in the systemd unit when ready for real
certificates, and open `https://your-domain`. Webhooks live at
`/hooks/{id}` on the same domain.

**Turning an old laptop into that server?** Full guide in
[docs/knowledge/server-setup.md](docs/knowledge/server-setup.md) — Ubuntu
install, lid/suspend power config (`deploy/laptop-server.sh`), home
network options (port-forward vs Cloudflare tunnel), and migrating data
off a dev machine.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `DEPLOYMATE_ADDR` | `127.0.0.1:8080` | listen address |
| `DEPLOYMATE_DATA_DIR` | `./data` | SQLite, keys, builds, repos |
| `DEPLOYMATE_SETUP_EMAIL` / `_PASSWORD` | — | create the owner user at startup |
| `DEPLOYMATE_LE_MODE` | `staging` | `production` for real Let's Encrypt certs |
| `DEPLOYMATE_RAILPACK` | `railpack` | path to the railpack CLI |
| `DEPLOYMATE_PREVIEW_HOST` | — | e.g. `dm.example.com`: every app gets a public `{slug}.{host}` subdomain routed by Host header (see docs/specs/cloudflare-tunnel.md) |
| `DEPLOYMATE_CLOUDFLARE_API_TOKEN` | — | auto-DNS: Cloudflare API token (scope `Zone.DNS:Edit`) that creates each new app's preview CNAME; requires the two vars below |
| `DEPLOYMATE_CLOUDFLARE_ZONE_ID` | — | auto-DNS: zone that owns the preview host |
| `DEPLOYMATE_CLOUDFLARE_TUNNEL_ID` | — | auto-DNS: named tunnel the preview CNAMEs target (`{id}.cfargotunnel.com`) |
| `DEPLOYMATE_BACKUP_DEST_<ID>_TYPE` | — | backups: registers destination `<id>` (`DEFAULT` = the default one); `s3` or `local` (dev/e2e only). No destination = backups UI disabled |
| `DEPLOYMATE_BACKUP_DEST_<ID>_ENDPOINT` / `_BUCKET` / `_ACCESS_KEY` / `_SECRET_KEY` | — | backups, `s3` type: bucket credentials (Cloudflare R2 works) |
| `DEPLOYMATE_BACKUP_DEST_<ID>_DIR` | — | backups, `local` type: target directory |

## Architecture notes

- **SQLite** metadata (`modernc.org/sqlite`, pure Go) with WAL + busy timeout;
  one connection, one deployment worker — builds serialize by construction.
- **Secrets at rest**: XChaCha20-Poly1305 under a 0600 key file in the data
  dir. Deploy keys, webhook secrets, env values, DB passwords.
- **Multi-server path**: everything above `internal/runtime` talks to the
  `Runtime` interface; a remote-agent impl is the future escape hatch.
- **Replicas** share one Traefik service named by a hash of its config,
  with a unique router per container — Traefik drops a router or service
  that two containers define differently (ADR 0018).
- **Security posture**: only Traefik publishes ports; app/db containers live
  on an internal bridge; webhooks verify HMAC + dedupe delivery IDs; the
  dashboard never executes user code on the host.

## Development

```sh
make gen           # regenerate templ files
make test          # unit tests
make vet
make e2e           # API smoke test against a running server
make e2e-manual    # image deploys (throwaway server)
make e2e-git       # webhook → build → swap (throwaway server)
make e2e-replicas  # scale, LB/failover, heal, rolling deploy
make e2e-backup    # backup → restore → retention (local destination)
make e2e-dns       # auto-DNS create/remove (needs real Cloudflare vars)
```

The `e2e-*` suites start their own throwaway server on a spare port with a
scratch data dir, and clean up after themselves.

**Handoff:** [progress.md](progress.md) is the project's checkpoint file —
where work stopped, what's next, and the reading path for anyone (human or
agent) picking the project up. Read it before anything else.

Layout: `cmd/deploymate` (binary), `internal/` (auth, store, runtime,
builder, jobs, services, proxy, monitor, sse, webhooks, crypto, config),
`web/` (Templ templates + vendored static assets), `deploy/` (bootstrap.sh,
systemd unit, Traefik config), `testdata/` (fixture repos + e2e scripts),
`docs/` (ADRs, knowledge base, specs, the backlog in `improvements.md`).

## Roadmap

The full ordered backlog lives in
[docs/improvements.md](docs/improvements.md). Highlights:

- First real-server run (Ubuntu 24.04, production certs) and server-side
  dashboard routing via Traefik's file provider
- Horizontal autoscaling (load-based replica count, scale-to-zero)
- Build cache export for Railpack builds (`--cache-to/--cache-from`)
- Per-runtime build/start command overrides
- Deploy previews per PR / per branch
- MySQL/Redis backups (Postgres ships today)
- Remote servers (the `Runtime` interface seam)
