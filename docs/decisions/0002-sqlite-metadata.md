# 0002 — SQLite for all metadata

- **Date:** 2026-08-28
- **Status:** accepted

## Context

The platform needs to persist users, projects, apps, services, deployments,
env vars, domains, metrics, and uptime checks. Single user, single server.

## Decision

**SQLite** via `modernc.org/sqlite` (pure Go, no CGO), with WAL +
`busy_timeout=5000` + `SetMaxOpenConns(1)`, migrated by `pressly/goose`
(embedded SQL, runs at startup). All IDs are UUID text; every app-facing
table carries `user_id`/`project_id` foreign keys so a Postgres migration
later is mechanical, not a rewrite.

## Consequences

- Zero database operations: the DB is one file in the data dir.
- `SetMaxOpenConns(1)` serializes writers; with one deployment worker
  (0006) and batched monitor inserts this is never a bottleneck.
- Cross-compilation stays trivial (no CGO).
- Empty strings violate nullable FKs — inserts must convert `""` to NULL
  (`internal/store/apps.go` documents the trap).

## Alternatives

- **Postgres** (Coolify/Dokploy style) — operational weight with no payoff
  at single-user scale; still the documented migration target.
- **Bbolt/Badger** — fine key-value stores, but SQL + goose gives
  migrations, ad-hoc queries, and a known escape hatch.
