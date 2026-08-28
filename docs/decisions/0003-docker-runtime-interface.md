# 0003 — Docker Engine as the compute substrate, behind a Runtime interface

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Apps, databases, and caches must run as containers. The platform must create,
start, stop, remove, inspect, log, exec, and stat containers — without
reimplementing an orchestrator.

## Decision

Use **Docker Engine** directly through the Go SDK
(`github.com/docker/docker/client`) — never Compose files, never the CLI for
runtime operations (the CLI is used only for `buildx` builds, see 0005).
All access goes through the `internal/runtime.Runtime` interface, the
single seam where a remote-agent implementation would plug in for
multi-server support later.

Container conventions: names are `dm-<slug>` (apps) and `dm-svc-<slug>`
(services); everything runs on the internal bridge `deploymate-net`
(0010); ownership labels `deploymate.managed/app/service` identify our
containers.

## Consequences

- The entire product is "write correct container specs" — Docker handles
  scheduling, restart policies, logging drivers, and image management.
- `Runtime` has one implementation today; the interface is the
  multi-server contract. Anything above `internal/runtime` must never
  import Docker packages directly.
- SDK client construction tries candidate endpoints in order
  (`DOCKER_HOST` → `/var/run/docker.sock` → Docker Desktop's
  `~/.docker/run/docker.sock`) because the CLI resolves contexts the SDK
  does not read.

## Alternatives

- **Docker Compose files** (Coolify-style) — declarative but means
  rendering YAML, reconciling state, and parsing output; direct API calls
  are typed and streamable.
- **Kubernetes/k3s** — real multi-server story, but heavy for one box and
  a large conceptual tax at this stage.
