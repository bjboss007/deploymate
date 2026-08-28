# 0006 — Deployment state machine + single in-process worker

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Deployments must survive restarts, be observable, and never contend with
themselves on one host's resources.

## Decision

Deployments are rows in the `deployments` table doubling as the queue:
`queued → building → running | failed`. Webhooks and the UI only *insert*
rows and respond fast; a **single in-process worker** polls the queue
(every second), atomically claims the oldest `queued` row
(`UPDATE ... WHERE status='queued'` guard), and processes it
synchronously — clone → build → swap container → mark running. A `kind`
column distinguishes `deploy` from `rollback` (skip build, reuse a kept
image tag).

The worker and the HTTP server share the process; only the worker claims
jobs, so **builds serialize by construction** — no BuildKit contention,
no queue dependency (no Redis).

## Consequences

- The state machine is fully observable: `status`, `error`,
  `started_at`, `finished_at`, `build_logs` per deployment.
- A crashed worker resumes by re-claiming stale `queued` rows after a
  restart (claims are guarded; `building` rows stuck by a hard kill
  remain — known gap, see improvements).
- One bad deploy cannot starve others; it can only delay them (single
  worker). Acceptable at personal scale, and the queue shape (rows, not
  messages) survives any future multi-worker design.

## Alternatives

- **Goroutine per deploy** — parallel builds on one box thrash CPU/IO.
- **Redis/Asynq queue** — another service to run; the SQLite queue is
  durable and free at this scale.
