# Alerts — specification

**Status:** implemented (Aug 2026) · ADR 0014 · Owner: solo

## Problem

Deployments fail silently, apps crash-loop, disks fill, uptime probes go
red — and the owner only learns by opening the dashboard. The system
already *produces* every event it needs; nothing notifies.

## Event catalog (v1)

| Event | Source | Payload |
|---|---|---|
| `deploy_failed` | worker, deployment → failed | app, deployment id, commit, error |
| `deploy_succeeded` | worker, deployment → running | app, deployment id, commit |
| `uptime_down` | monitor, probe ok→not-ok transition | domain, status code/latency |
| `uptime_recovered` | monitor, not-ok→ok transition | domain, latency |
| `container_restart` | monitor, RestartCount increased | app, restart count |
| `disk_almost_full` | monitor, ≥90% disk usage (once/24 h) | bytes used/total |

Deliberately excluded from v1: per-domain-cert expiry (TLS status sync
doesn't exist yet), memory/cpu thresholds (needs baselines), and
deploy-queued noise. Healthchecks, when they land, will emit
`app_unhealthy`/`app_recovered` through the same dispatcher — the event
catalog is the extension point.

## Channels (v1: webhook only)

Generic JSON webhook POST — Slack-compatible format, so a Slack incoming
webhook works unchanged:

```json
{
  "text": "[deploymate] deploy failed: Web Front (commit abc1234) — build failed: exit status 1",
  "attachments": [{ "color": "danger|good|warning", "title": "<event>", "text": "<details>" }]
}
```

Email and Telegram are follow-ups: the `alerts` row carries a `channel`
column, and the dispatcher switches on it, so adding a channel is a new
case in one file plus a config form field.

## Storage

- `alerts`: one row per notification target — channel, encrypted
  endpoint URL (XChaCha20, same envelope as everything else), enabled
  flag, subscribed events (JSON array).
- `alert_events`: delivery history per alert — event, subject, ts,
  delivered, HTTP status, error. Bounded (pruned hourly, 30 d) and
  visible in the UI so a dead webhook is diagnosable without logs.

## Delivery semantics

- Best-effort, synchronous from the emitter (worker/monitor goroutines
  are not HTTP handlers — blocking 5 s is fine).
- 5 s timeout, one retry, then record failure and move on. Never
  back-pressure the deploy pipeline.
- Cooldown per (alert, event-type): `disk_almost_full` at most once/24 h,
  `container_restart` once/5 min per app, `uptime_*` only on transitions
  (never per-probe) — the dedup lives in the sources, not the dispatcher.

## UI

- `/alerts` page (topbar link): list targets with event chips, add/delete
  webhook, last-10 deliveries per target with status codes.
- No alerting config on any other page.

## Verification

E2e: local python receiver on 127.0.0.1:8777 → create webhook alert via
API → trigger a failing deploy (bad Dockerfile push) → assert receiver
got the POST, `alert_events` row says delivered=1 → stop the receiver,
trigger again → row says delivered=0 with error. Uptime transition:
delete a domain's target reachability (point at dead host) → next probe
fires `uptime_down`.
