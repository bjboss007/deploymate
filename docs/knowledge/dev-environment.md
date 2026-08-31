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

- **Traefik can't be tested locally**: Docker Desktop cannot mount
  `~/.docker/run/docker.sock` into containers, so the docker provider
  never sees the daemon. `deploy/traefik/dev.yml` exists for Linux dev
  boxes. On macOS, verify the label contract via `docker inspect` (the
  unit tests in `internal/proxy` cover generation) and do the real
  router/cert test on the server.
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
