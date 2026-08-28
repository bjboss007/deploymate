# 0013 — Selectable runtime environments via Railpack

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Dockerfile-only builds force every user to write one. The product should
let you pick a runtime for known languages — "Node.js 22", "Python 3.13",
"Go" — and have the platform produce the image.

## Decision

**Railpack** (Railway's open-source Go buildpack, successor to Nixpacks)
as a second build path alongside Dockerfile:

- `apps.runtime` holds a spec (`""` = Dockerfile, else `key[:version]`,
  e.g. `node:22`). The app page has a Build panel: method dropdown
  (Dockerfile + 11 runtimes: Node.js, Python, Go, Ruby, PHP, Java, Rust,
  Deno, Elixir, .NET, Static site) and an optional version field.
- The worker runs `railpack build <dir> --name <tag> --progress plain`
  as a subprocess — same pattern as buildx — with
  `BUILDKIT_HOST=docker-container://dm-buildkit` pointing at a
  `moby/buildkit` container (BuildKit's docker-container connhelper;
  started by `bootstrap.sh` alongside Traefik).
- Version pinning is uniform across providers: if the user pins a
  version and the repo has no `.mise.toml`, we write one (`[tools] node
  = "22"`) into the checkout; otherwise Railpack auto-detects from the
  repo's own version files (`.node-version`, `.python-version`, mise
  config) — the repo always wins.
- Railpack's default output path pipes the image into `docker load`, so
  the built image lands in the local daemon under our tag and the
  existing container-swap code is unchanged.

## Consequences

- Two build engines, one pipeline: clone → (detect) → build → swap;
  both stream build lines into `build_logs` + SSE identically.
- `bootstrap.sh` now installs the railpack binary (pinned
  `RAILPACK_VERSION=v0.38.0` from GitHub releases) and runs the
  `dm-buildkit` container. Dev machines need the same two things.
- Railpack is a subprocess dependency, deliberately not imported as a
  Go library: the CLI is the stable interface, upgrades are a version
  bump in bootstrap, and our binary stays lean.
- Runtime-selected builds work without a Dockerfile; Dockerfile stays
  the default and the escape hatch (a repo with both uses whichever
  method is selected).

## Alternatives

- **Nixpacks** — its predecessor; maintenance-mode since Railpack
  shipped, bigger images (Python ~1.5 GB vs ~0.35 GB), and version
  pinning is a patch-table hack.
- **Import railpack as a Go library** — tighter, but couples our build
  to their API churn and balloons the binary; the CLI gives us the same
  streaming and the docker-load behavior for free.
