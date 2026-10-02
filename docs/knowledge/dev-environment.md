# Dev environment

## Prerequisites

- Go 1.24+ (built on 1.26)
- Docker Desktop (or any docker daemon) — used for real deployments in dev
- `templ` CLI: `go install github.com/a-h/templ/cmd/templ@latest`
  (lives at `$(go env GOPATH)/bin/templ`, often **not on PATH** — the
  Makefile resolves it)
- `ssh-keygen`, `git` (deploy keys + clones)

## Running

```sh
make dev     # http://127.0.0.1:8090, data in ./data
# first user:
DEPLOYMATE_DATA_DIR=./data ./bin/deploymate setup-admin   # interactive, or env:
DEPLOYMATE_SETUP_EMAIL=you@example.com \
DEPLOYMATE_SETUP_PASSWORD=secret \
DEPLOYMATE_ADDR=127.0.0.1:8090 ./bin/deploymate serve
```

## Why port 8090 locally

Docker Desktop occupies 8080. On the server the default `127.0.0.1:8080`
applies (Traefik proxies to it).

## Docker socket resolution

The docker CLI resolves *contexts* (`docker context show`); the Go SDK
reads only `DOCKER_HOST`. `runtime.NewDocker` tries, in order:
`DOCKER_HOST` (with TLS settings) → `unix:///var/run/docker.sock` →
`unix://$HOME/.docker/run/docker.sock` (Docker Desktop's macOS socket).
On Ubuntu only the second exists.

## Docker Desktop disk

Docker Desktop's virtual disk filled up once and database init started
failing with bizarre errors (MySQL: "UUID failed", ENOSPC) — took an hour
to diagnose. The daemon cannot report host-disk free space, so watch the
**growable** storage instead:

- **/stats → Storage panel**: live `docker system df` totals (images,
  containers, volumes, build cache, reclaimable), every named volume with
  its size (untracked ones flagged — that is the diagnosis), and the 5
  largest images. Loaded on page view; a daemon hiccup shows a warning
  note, never a 500.
- **The growth alert** (`disk_almost_full`, once/24 h) fires at 20 GiB of
  images + build cache — `internal/monitor` `checkDisk`; volume bytes are
  deliberately excluded (user data).
- Reclaim: `docker system prune` (safe, keeps running containers) or
  `docker system prune -a` (also drops unused images). DeployMate keeps
  the newest 5 rollback images per app; rebuilding an app regenerates them.
  `images.size_bytes` (filled at build time) feeds the "tracked app
  images" card, which survives daemon pruning.

## Build & test

```sh
make gen     # templ generate (checked-in _templ.go means go build works without it)
make build   # bin/deploymate
make test    # unit tests
make vet
make e2e     # API smoke test against a running server (login→project→app→deploy nginx)
make e2e-git # git-deploy path on a self-contained throwaway server (see below)
```

## Known macOS-only limitations

- **Traefik on Docker Desktop: mount the VM socket, not the host one.**
  `~/.docker/run/docker.sock` can't be mounted into containers, but
  `-v /var/run/docker.sock:/var/run/docker.sock` (the path inside
  Docker Desktop's VM) works, and the docker provider sees every
  container. That's how the replicas label scheme was spiked (2026-09-30,
  `traefik:v3.3` + `traefik/whoami`; see docs/specs/app-replicas.md
  "Spike results"). Recipe: `docker network create spike-net`, run
  `traefik:v3.3` on it with `--providers.docker.network=spike-net
  --providers.docker.exposedbydefault=false --entrypoints.web.address=:80
  --api.insecure=true`, publish 80/8080 on loopback, then read
  `/api/http/routers` and `/api/http/services` for status. The
  `deploy/traefik/dev.yml` config still assumes a Linux box. TLS/cert
  issuance remains a server-only test.
- Let's Encrypt issuance requires a public IP + DNS anyway — staging or
  production.

## Fixture repos for e2e

`testdata/repos/site/` is a tiny nginx static site used to exercise the
git pipeline: push it to a local bare repo, point a git_source's
`repo_url` at the bare path in SQLite (the UI validates SSH/HTTPS URLs
only), and deploy. `testdata/apps/dbprobe/` is a Go image that queries
Postgres via the injected `DATABASE_URL` (build it with
`docker build -t dbprobe:latest testdata/apps/dbprobe`).

## `make e2e-git` — the git-deploy path, self-contained

`testdata/e2e_git.sh` exercises the real product flow end to end against a
**throwaway** server (spare port 18091, scratch data dir, its own owner),
so it never touches the live :8090 apps. It builds a local bare repo from
`testdata/repos/e2e-web/` (an nginx Dockerfile serving on the platform
port 8080, so the worker's default port binding publishes it with no
app-side config), links it via `seed-git-source`, fires a **signed GitHub
webhook**, waits for the worker to clone→build→swap, then HTTP-probes the
app through `/preview/{slug}/`. Everything (server, container, built image,
temp repos) is cleaned up on exit.

Two things make this scriptable where the UI can't be:
- **`deploymate seed-git-source <app-slug> <repo-url> [branch] [provider]`**
  (`cmd/deploymate/seed.go`) — a test-seeding subcommand, the git twin of
  `setup-admin`. It links an app to *any* repo URL (including a local bare
  path the HTTP connect handler rejects), generating a real deploy key +
  webhook secret and printing `source_id=` / `webhook_secret=` so a caller
  can sign a webhook. Needs `DEPLOYMATE_DATA_DIR` pointed at the server's
  data dir (shared master key).
- The app **slug is timestamped** (`e2eweb<epoch>`): the Docker daemon is
  shared with the user's real server, so a fixed `dm-web` name would clobber
  a live container. Timestamping keeps the container name, preview port
  (crc32 of slug), and image tag unique.

Deliberately passes **no** `DEPLOYMATE_CLOUDFLARE_*` vars, so the throwaway
never creates real DNS records. Uses the Dockerfile build engine (no
Railpack/buildkit dependency) to stay portable and fast.

## `make e2e-artifact` — prebuilt (GitHub Actions artifact) deploys

`testdata/e2e_artifact.sh` drives the whole prebuilt pipeline on a throwaway
server (:18102) against a **fake GitHub** (`testdata/fakegithub.py`, ports
18103 API / 18104 storage; Python 3 stdlib only). The server reaches it
through the **test-only** env var `DEPLOYMATE_GITHUB_API_URL` (default
`https://api.github.com`; never set it on a real server). The fake mirrors
the response shapes captured from real GitHub (spike S1): Bearer auth,
artifact lists with a zip `digest`, a 302 to a separate storage host that
**rejects any request carrying an Authorization header**, 401/404 bodies.

- Fixture: `testdata/apps/hellojar/` — `Hello.java` + `build.sh` build a
  1.6 KB `hello.jar` with a JDK in Docker (committed; rebuild only when
  `Hello.java` changes). It answers "deploymate e2e prebuilt jar fixture".
- Needs `eclipse-temurin:21-jre` (pulled once if absent), `python3`, Docker.
- Seeding without the UI: `seed-git-source` reads `DEPLOYMATE_SEED_MODE=
  artifact`, `DEPLOYMATE_SEED_API_TOKEN`, `_WORKFLOW`, `_ARTIFACT`.
- **Gotcha:** a leftover fake/server on 18102–18104 makes a run fail for the
  wrong reason (the stale fake answers with *its* token → 401). The script
  now refuses to start when those ports are busy. (Don't use `kill %1` in a
  non-interactive shell — it doesn't kill the job.)
- Quick tunnel note (from the S1 spike): with `~/.cloudflared/config.yml`
  present a `cloudflared tunnel --url` quick tunnel answers 404 unless given
  an empty `--config`; new trycloudflare hostnames can take ~80 s to resolve.
