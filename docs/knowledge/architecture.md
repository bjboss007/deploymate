# Architecture

## The whole system on one page

```
        GitHub / GitLab (webhooks, HMAC)            browser
              │                                      │ https://app.example.com
              ▼                                      ▼
┌───────────────────── Ubuntu host ─────────────────────────────────────────┐
│  deploymate (systemd Go binary, :8080 bound to 127.0.0.1)                 │
│  ┌───────────────┐ ┌──────────────┐ ┌────────────┐ ┌──────────────────┐   │
│  │ httpserver    │ │ jobs worker  │ │ monitor    │ │ store (SQLite)   │   │
│  │ chi + Templ   │ │ single loop  │ │ 5s/30s/1h  │ │ WAL, 1 conn      │   │
│  │ + SSE broker  │ │ queue claims │ │ samplers   │ │ goose migrations │   │
│  └──────┬────────┘ └──────┬───────┘ └─────┬──────┘ └────────┬─────────┘   │
│         │        runtime.Runtime (Docker SDK)   crypto (XChaCha20)        │
│         └───────────────┬───────────────────────┘   builder (buildx)      │
│                         │ /var/run/docker.sock                            │
│  /var/lib/deploymate/{data.db, keys/, builds/, repos/, letsencrypt/}      │
│   ┌────────────────── Docker Engine ──────────────────────────────────┐   │
│   │  deploymate-net (internal bridge)                                 │   │
│   │   dm-<app>   dm-<app>   dm-svc-<slug>   traefik ── 80/443 (only)  │   │
│   └───────────────────────────────────────────────────────────────────┘   │
└────────────────────────────────────────────────────────────────────────────┘
```

## Components

| Component | Package | Job |
|---|---|---|
| HTTP server | `internal/httpserver` | chi router: dashboard (authed), `/hooks/{id}` (HMAC-authed), SSE streams, JSON data endpoints |
| Auth | `internal/auth` | argon2id passwords, cookie sessions (token hash in DB), CSRF tokens |
| Store | `internal/store` | all SQL; one `*sql.DB` with `SetMaxOpenConns(1)`; goose migrations embedded |
| Runtime | `internal/runtime` | `Runtime` interface; Docker impl (containers, networks, images, logs, exec, stats) |
| Worker | `internal/jobs` | claims queued deployments; clone → build → swap → mark; rollbacks; image pruning |
| Builder | `internal/builder` | Dockerfile detection + buildx invocation with line streaming |
| Git | `internal/gitpkg` | deploy-key generation (ssh-keygen), clone/pin via git CLI |
| Webhooks | `internal/webhooks` | HMAC verify, payload parse, delivery dedup |
| Proxy | `internal/proxy` | Traefik label generation (pure, unit-tested) |
| Services | `internal/services` | Postgres/MySQL/Redis templates: image, env, conn URL, readiness cmd |
| Monitor | `internal/monitor` | stats sampler, uptime prober, retention pruner |
| SSE | `internal/sse` | pub/sub broker (deploy events) + stream helpers |
| Crypto | `internal/crypto` | XChaCha20-Poly1305 envelopes + key file |
| Web | `web/` | Templ templates (`web/templates`), vendored static assets (`web/static`, embedded) |

## Data flow: git push → live URL

1. GitHub POSTs `/hooks/{id}` → HMAC check → dedup → branch filter → insert
   `deployments` row (queued) → 200 in <50 ms.
2. Worker claims the row (atomic status guard) → `building`.
3. Clone at SHA (deploy key, temp 0600 file) → detect Dockerfile →
   `docker buildx build --load` with every line appended to `build_logs`
   and fanned out to the SSE topic `deploy:<slug>`.
4. Tag `deploymate/apps/<slug>:<deployment_id>`; record in `images`.
5. Swap: stop/remove `dm-<slug>` → create+start with full env (service
   URLs + app vars + `GIT_SHA`) and Traefik labels → `running`.
6. Prune images beyond the newest 5 per app.

Failure at any step → `failed` with `error` recorded; **the old container
keeps serving** — the swap only happens after a successful build.

## The two growth seams

1. **`runtime.Runtime`** — multi-server = a new implementation (SSH/gRPC
   agent); nothing above the interface may import Docker packages.
2. **Schema** — every app-facing table carries `user_id`/`project_id`
   FKs and roles exist in `users`; multi-user = policy code, not schema
   surgery. Postgres migration path: goose SQL is portable.

## What the UI talks to

| Endpoint | Kind | Auth | Purpose |
|---|---|---|---|
| `/login`, `/logout`, `/projects/*`, `/apps/*`, `/services/*`, `/deployments/*` | HTML + forms | session + CSRF | dashboard |
| `/apps/{slug}/logs` | SSE | session | live container logs (direct docker tail) |
| `/deployments/{id}/stream` | SSE | session | build log replay + live deploy events |
| `/apps/{slug}/metrics`, `/apps/{slug}/uptime` | JSON | session | charts / uptime data |
| `/hooks/{id}` | POST | webhook secret | push events |
| `/healthz` | GET | none | liveness |
