# 0010 — Internal bridge network; only Traefik publishes ports

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Docker's iptables rules bypass ufw, so host-firewall-only protection is
illusory once containers publish ports. Apps and databases must be
unreachable from the internet except through the proxy.

## Decision

All DeployMate containers (apps, services, Traefik) join the internal
bridge `deploymate-net`. **No container except Traefik publishes host
ports** — Traefik owns 80/443 (ufw-guarded). Apps reach each other and
services by container name DNS on that network; services are addressed at
`dm-svc-<slug>:<port>` in injected connection URLs. The network is *not*
docker-internal because app builds need outbound internet.

## Consequences

- The "Docker bypasses ufw" risk is neutralized by construction, not by
  configuration discipline.
- `DATABASE_URL` etc. never leave the box and never traverse the public
  interface — `sslmode=disable` is therefore correct for managed
  Postgres (documented in `internal/services/templates.go`).
- Adding a new public service requires an explicit decision (edit
  bootstrap/Traefik config), which is exactly the friction we want.

## Alternatives

- **Publish per-app ports + ufw rules** — fragile; Docker rewrites
  iptables on daemon restart.
- **docker network internal** — blocks outbound internet from
  containers, breaking builds and app dependencies.
