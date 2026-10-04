# 0020 — API tokens: bearer-only /api, hashed at rest, read-only by default

- **Date:** 2026-10-04
- **Status:** accepted

## Context

The dashboard only had browser sessions. An MCP server (and any script) needs
programmatic access that does not borrow a browser login, can be limited, and
can be revoked without changing the password.

## Decision

- **Personal access tokens** (`dm_` + 43 URL-safe chars from 32 random bytes),
  created on `/settings/tokens`. The plaintext is shown **once**, on the response
  to the create request (`Cache-Control: no-store`, never in a URL); the database
  keeps only its SHA-256 and a 9-character prefix to recognise it by (migration
  0020). A token acts as its creator and is deleted with that user.
- **Three scopes, each including the last** (revised the same day, when the MCP
  plan added provisioning): `read` (GET/HEAD only), `deploy` (act on apps that
  exist), `provision` (create and configure). The form defaults to read-only;
  the read/non-read split is enforced centrally in `auth.RequireAPIToken` and
  the finer tiers per route group by `auth.RequireScope`, so a new `/api/v1`
  route cannot forget it. A legacy `write` token ranks as `deploy`.
- **Expiry** is chosen at creation (30 d / 90 d (default) / 1 y / never); an
  unparseable expiry counts as expired. Revoking deletes the row, effective on
  the next request. At most 20 tokens per user.
- **`/api/v1` accepts bearer tokens only — never the session cookie.** A browser
  the owner is logged in to must not be able to drive the API through a
  cross-site request (the session forms rely on CSRF tokens; bearer headers
  need none). Failures are JSON and never say *why* a token failed.
- **Out of reach for any token, by design:** reading secret values (variables
  list names and a masked flag; a provision token may *set* them, never read
  them back), deleting anything, connection strings, key and token management.
  Those stay session-only.

## Consequences

- A leaked token is bounded by its scope and expiry and is revocable at
  `/settings/tokens`; `dm_` makes it recognisable to secret scanners.
- `last_used_at` is written at most once a minute per token.
- Every state-changing API call is audited (`audit_log`, migration 0021) and shown
  on the tokens page and in the app's history; writes are rate-limited per token
  (20/min, 200/h) — a looping agent hits a wall, not the platform.
- Built on top: the read API and `deploymate mcp` (docs/specs/mcp-and-api.md).
