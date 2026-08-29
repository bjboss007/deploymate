# 0016 — Env var aliasing: ${KEY} references in env values

- **Date:** 2026-08-29
- **Status:** accepted

## Context

VGG's Heroku profile expects a MySQL connection string under the key
`DATABASE_URL`, but the platform injects MySQL as `MYSQL_URL` (one fixed
key per service type). The workaround was copying the connection string
by hand — which bakes credentials into config and breaks on rotation.

## Decision

Env var values may reference any other variable in the computed
environment: `DATABASE_URL = ${MYSQL_URL}`. Expansion happens at deploy
time inside `AppEnv` (the single env-assembly path shared by the worker
and manual deploys), against the full map — service-injected URLs plus
the app's own variables — with chained references (up to 5 passes) and a
cycle guard. Unresolved references are **left as-is**: a typo must be
visible in the container env and logs, never silently drop a variable.

## Consequences

- Any injected URL maps to any key without new UI, new tables, or new
  injection rules — the mapping is just data.
- App vars still win over service-injected keys on duplicates, and
  ordering is deterministic (sorted) in the container env.
- Verified: VGG's `DATABASE_URL` switched from a copied literal to
  `${MYSQL_URL}`; the redeployed container env shows the expanded
  mysql:// URL and the app stays healthy.

## Alternatives

- **Per-app injection mapping UI** (choose which key each service type
  injects) — more explicit, but a whole settings surface for what a
  one-line reference expresses; aliasing subsumes it.
- **Inject all aliases always** — ambiguous the moment two database
  services run in one project (both would claim DATABASE_URL).
