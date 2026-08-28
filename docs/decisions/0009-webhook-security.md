# 0009 — Webhook security: HMAC + delivery dedup + fast response

- **Date:** 2026-08-28
- **Status:** accepted

## Context

`POST /hooks/{source_id}` is public (no login) and triggers builds. It must
reject forgeries, tolerate provider retries without double deploys, and
answer fast so providers don't resend.

## Decision

- **GitHub:** verify `X-Hub-Signature-256` (HMAC-SHA256 over the raw
  body, `hmac.Equal` constant-time); body is read once with
  `io.LimitReader` (1 MiB cap).
- **GitLab:** constant-time comparison of `X-GitLab-Token`.
- Per-source webhook secret, generated at git-connect time and stored
  encrypted (0008).
- **Delivery dedup:** in-memory cache keyed
  `provider:delivery-id` (GitHub `X-GitHub-Delivery`, GitLab
  `X-Gitlab-Event-UUID`), 24 h TTL — dedup survives server restarts
  poorly (in-memory), which is acceptable because duplicate deliveries
  produce a queued deploy of the same SHA.
- The handler only *queues* a deployment and returns `200` immediately
  (sub-50 ms); the worker does the real work. Branch filter: only
  `refs/heads/<default_branch>` deploys.

## Consequences

- Provider retries/double-sends never double-deploy within 24 h.
- Webhook endpoints never run repo code on the host — builds happen
  inside containers via the worker.
- Payload parsing is minimal (ref, after, head message) — no library
  dependency on provider SDKs.

## Alternatives

- **Per-provider SDK webhook verification** — heavier dependencies for
  what is 30 lines of HMAC code.
