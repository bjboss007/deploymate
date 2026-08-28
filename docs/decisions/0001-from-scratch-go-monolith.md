# 0001 — Build from scratch in Go, as a single-binary monolith

- **Date:** 2026-08-28
- **Status:** accepted

## Context

DeployMate is a personal self-hosted PaaS: deploy, monitor, and connect git
sources for apps, databases, and caches on one baremetal server. The founder
considered forking Coolify/Dokploy but wanted full ownership and the learning
surface of building it.

## Decision

Build from scratch in **Go** as a **single binary** that runs directly on the
host as a systemd service, talking to the local Docker socket. No
control-plane/agent split, no docker-in-docker. Personal/single-user first,
but two seams must not block growth: the `Runtime` interface (multi-server
later) and user/project FKs in the schema (multi-user later).

## Consequences

- One ~20 MB static binary + a data directory is the entire install.
- Direct socket access = no container escaping or socket-mounting problems
  for the platform itself.
- Every phase of the MVP (apps, services, git deploys, domains, monitoring)
  ships in the same process — an in-process worker is a feature, not a hack
  (see 0006).

## Alternatives

- **Fork Coolify/Dokploy** — 80% of features day one, but inherits a large
  codebase and their architecture (Postgres, Docker Compose files,
  multi-agent complexity).
- **Rust/Python/TypeScript** — Go wins on single-binary distribution, the
  Docker SDK ecosystem, and long-running daemons/SSE.
