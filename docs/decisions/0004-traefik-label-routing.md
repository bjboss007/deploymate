# 0004 — Traefik v3, label-driven routing, Let's Encrypt staging by default

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Apps need domains and automatic HTTPS. The proxy must pick up new apps and
routes without restarts, and certificate failures must never break deploys.

## Decision

**Traefik v3** as a container on `deploymate-net` — the *only* container
publishing host ports (80/443). DeployMate writes routing labels onto app
containers (`traefik.http.routers.<slug>.rule`, `.tls`, `.tls.certresolver`,
`...services.<slug>.loadbalancer.server.port`); Traefik's docker provider
discovers them with zero proxy restarts. Two ACME resolvers exist
(`staging`, `letsencrypt`); DeployMate uses **staging by default** and
production only when `DEPLOYMATE_LE_MODE=production`. Cert issuance is
asynchronous and never blocks a deploy.

Label generation is centralized in `internal/proxy` and unit-tested —
that package *is* the routing contract.

## Consequences

- DeployMate never generates proxy config files at runtime; the static
  Traefik config is installed once by `bootstrap.sh`.
- The docker socket is mounted read-only into Traefik (hardening idea:
  `tecnativa/docker-socket-proxy` — see improvements).
- Routing labels only exist when the app has domains; unexposed containers
  stay unreachable from outside.
- Staging certs are untrusted by browsers — the uptime prober therefore
  skips TLS verification so staging deployments still show healthy.

## Alternatives

- **Caddy** — simpler config language, but no first-party docker
  provider (relies on the third-party `caddy-docker-proxy`).
- **Nginx + certbot** — requires writing config files and reloading on
  every route change; workable, more moving parts.
