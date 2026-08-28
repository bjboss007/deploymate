# 0007 — HTMX + Templ dashboard served from the binary

- **Date:** 2026-08-28
- **Status:** accepted

## Context

The platform needs a dashboard: auth, project/app/service CRUD, live logs,
deployment views, charts. One maintainer; the binary must stay
self-contained.

## Decision

**Templ** (type-checked Go templates) + **HTMX** (form posts, SSE log
streaming) + **Alpine-free vanilla JS** for the two charts, all served
from the same Go binary; Chart.js, htmx, fonts, and CSS are vendored in
`web/static` (no CDN calls at runtime). Server-Sent Events for logs and
deployment progress (one-directional — WebSockets are overkill).
Every data endpoint is plain JSON (`/apps/{slug}/metrics`,
`/apps/{slug}/uptime`, …), so a React SPA can replace the dashboard
without touching the backend.

## Consequences

- ~100 KB of JS total; the dashboard works with JS disabled for CRUD.
- `templ generate` is a build step (`make gen`) — checked-in `_templ.go`
  files keep `go build` working without the templ binary.
- SSE handlers must demux docker log streams with `stdcopy` and close
  the reader on client disconnect, or tails leak.

## Alternatives

- **React/Vite SPA** — richer interactions, but a second toolchain, a
  build pipeline, and API auth/token plumbing for a single user's
  dashboard.
