# 0011 — Monitoring: SQLite sampling at fixed cadences

- **Date:** 2026-08-28
- **Status:** accepted

## Context

MVP monitoring = per-app CPU/memory charts, live logs, and uptime
history. No Prometheus/ClickHouse is justified at single-box scale.

## Decision

One `monitor` loop with three cadences:
- **5 s:** sample Docker stats for every running app
  (`ContainerStats(stream=false)` → `metrics` table; CPU% computed as
  usage-delta/system-delta × **`online_cpus`** — the authoritative field;
  `percpu_usage` is absent on Docker Desktop).
- **30 s:** HTTPS-probe every domain → `uptime_checks` (ok, status
  code, latency). TLS verification is skipped so staging LE certs
  (0004) probe healthy; OK = any response < 500.
- **1 h:** prune `metrics` (>7 d) and `uptime_checks` (>30 d).

Charts read the newest 60 samples over JSON; uptime history renders as
30 server-side dots. Logs are live-only (SSE tail), not archived.

## Consequences

- SQLite write volume ≈ 12 rows/min/app + 2 rows/min/domain — trivial.
- `metrics`/`uptime_checks` are append-only with indexed (app_id, ts) —
  the query pattern is "last N", which the subquery trick serves.
- No metrics infra to run or babysit; a future `/metrics` Prometheus
  endpoint would be additive.

## Alternatives

- **Prometheus + Grafana** — right when this becomes multi-server or
  multi-user; premature now.
- **cAdvisor** — heavier, and its API is another integration surface;
  the Docker stats API is the documented programmatic source.
