# 0014 — Alerts: event catalog + webhook channel, best-effort delivery

- **Date:** 2026-08-29
- **Status:** accepted

## Context

The platform already detects everything worth alerting on (deploy
failures in the worker, uptime transitions in the monitor, restart
counts, disk usage) but nothing notifies the owner.

## Decision

1. **An explicit event catalog** (`deploy_failed`, `deploy_succeeded`,
   `uptime_down`, `uptime_recovered`, `container_restart`,
   `disk_almost_full`) — emitters translate their state transitions into
   catalog events; the dispatcher is dumb.
2. **One channel in v1: generic webhook**, Slack-compatible payload so
   Slack/Teams-compatible/Discord webhooks work unconfigured. The
   `channel` column + dispatcher switch make email/Telegram additive.
3. **Best-effort, synchronous delivery** (5 s timeout, one retry) from
   the worker/monitor goroutines — notifications must never back-pressure
   deploys. Every attempt is recorded in `alert_events` (30 d retention)
   and shown in the UI.
4. **Dedup lives in the sources**: uptime alerts fire on transitions
   only; disk fires at most daily; restart alerts per-app with a
   cooldown. The dispatcher never dedupes.
5. Endpoint URLs encrypted at rest like every other secret.

## Consequences

- Alerts are visible and debuggable from the dashboard without log
  access — a dead webhook shows failed deliveries with status codes.
- Healthchecks (future) emit into the same catalog; no redesign needed.
- Deliberately no queues/workers for delivery: a second in-process
  dispatcher would need its own retry/backoff story for zero gain at
  single-user scale.

## Alternatives

- **Full event bus (NATS/Redis)** — the broker pattern exists for SSE
  already; a durable bus is warranted only when delivery guarantees
  matter beyond one owner's Slack.
- **Prometheus Alertmanager** — powerful, but drags in a second stack
  and its own storage; our events are app-level, not metric-level.
