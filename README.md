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
  one-click rollback (last 5 images kept). Build from a **Dockerfile** or
  select a **runtime** — Node.js, Python, Go, Ruby, PHP, Java, Rust, Deno,
  Elixir, .NET, static sites — built with Railpack, no Dockerfile needed,
  optionally version-pinned
- **Databases & caches** — one-click Postgres 16, MySQL 8, Redis 7 with
  generated passwords (encrypted at rest), named volumes, readiness checks,
  and automatic connection-string injection (`DATABASE_URL`, `MYSQL_URL`,
  `REDIS_URL`) into every app in the project
- **Environment variables** — per-app, encrypted at rest, secrets masked in
  the UI
- **Domains & HTTPS** — automatic Let's Encrypt via Traefik (staging
  resolver by default), label-driven routing with zero proxy restarts
- **Preview URLs** — every running app gets `/preview/<slug>` on the
  dashboard instantly, before any domain exists
- **Monitoring** — CPU/memory charts (5s sampling), live container logs over
  SSE, and 30s uptime probes per domain with history

## Run locally (dev)

```sh
make dev                      # http://127.0.0.1:8090 (Docker Desktop)
DEPLOYMATE_EMAIL=you@example.com DEPLOYMATE_PASSWORD=secret \
  ./bin/deploymate setup-admin   # or: make build first
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

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `DEPLOYMATE_ADDR` | `127.0.0.1:8080` | listen address |
| `DEPLOYMATE_DATA_DIR` | `./data` | SQLite, keys, builds, repos |
| `DEPLOYMATE_SETUP_EMAIL` / `_PASSWORD` | — | create the owner user at startup |
| `DEPLOYMATE_LE_MODE` | `staging` | `production` for real Let's Encrypt certs |

## Architecture notes

- **SQLite** metadata (`modernc.org/sqlite`, pure Go) with WAL + busy timeout;
  one connection, one deployment worker — builds serialize by construction.
- **Secrets at rest**: XChaCha20-Poly1305 under a 0600 key file in the data
  dir. Deploy keys, webhook secrets, env values, DB passwords.
- **Multi-server path**: everything above `internal/runtime` talks to the
  `Runtime` interface; a remote-agent impl is the future escape hatch.
- **Security posture**: only Traefik publishes ports; app/db containers live
  on an internal bridge; webhooks verify HMAC + dedupe delivery IDs; the
  dashboard never executes user code on the host.

## Development

```sh
make gen    # regenerate templ files
make test   # unit tests
make vet
make e2e    # API smoke test against a running server
```

Layout: `cmd/deploymate` (binary), `internal/` (auth, store, runtime,
builder, jobs, services, proxy, monitor, sse, webhooks, crypto, config),
`web/` (Templ templates + vendored static assets), `deploy/` (bootstrap.sh,
systemd unit, Traefik config), `testdata/` (fixture repos + e2e script).

## Roadmap

- Build cache export for Railpack builds (`--cache-to/--cache-from`)
- Per-runtime build/start command overrides
- Remote servers (the `Runtime` interface seam)
- Scheduled/periodic deployments, deploy previews per PR
- Backups for database volumes
- Disk usage dashboard (`docker system df`)
