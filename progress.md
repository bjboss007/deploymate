# Progress — DeployMate

> **Handoff file.** First thing any agent reads; last thing updated before
> stopping. Read this whole file (~1 minute), then follow the pointers for
> the task at hand — do **not** scan the repo to orient. If a pointer below
> doesn't answer something, read `docs/knowledge/architecture.md` next.

## Where we stopped

**2026-08-29** — **App action feedback + status-badge bug fix** (browser-
verified). App start/stop/restart use HTMX to swap only `#head-actions`, so
the `?flash=` confirmation those handlers set never rendered — no page
reload. Fixes (`web/templates/apps.templ`, `web/static/app.css`): the
status badge now lives inside the swapped region so every action visibly
flips it; `hx-disabled-elt="find button"` + a CSS spinner give an in-flight
state (a stop can take ~10s). While verifying, found a **pre-existing
bug**: `statusBadge`/`healthBadge` used `@statusDot("running"){ status }`,
and templ read the `{ status }` as a literal children-text block — every
badge across the app showed the word "status" (and health showed
`"healthy"` with quotes) instead of the value. Fixed by passing the label
as a `statusDot` parameter. Verified live via curl against the running
server (minted+deleted a dev session): badge reads `running`; POST /stop
returns the partial with badge `stopped` + Start button; /start restores.
**A running server on :8090 is the freshly-built binary I restarted** (was
PID 74778).

**2026-08-29** — **Infra manifest teardown (surface-only)** shipped
(unit-tested, not yet e2e). A manifest-created service the manifest stops
declaring is flagged orphaned — badge on the service card, a build-log
line, and a `service_orphaned` event — but **never deleted** (the
manifest's "deleting infra is a human decision" rule). The human Keeps it
(adopts → origin `manual`, `POST /services/{slug}/keep`) or Deletes it.
Manual services are never flagged; dropping the whole manifest flags
nothing. Migration 0010 adds `services.origin` + `services.orphaned`;
`Provisioner.reconcileOrphans` does the diff. Spec + backlog updated.
Next manifest item: **seeding/backups** (overlaps Database backups).
**Not verified:** real deploy exercising the orphan path (unit tests
cover flag/clear/manual-immunity). **Not yet committed.**

**2026-08-29** — **Environments + image pins** shipped and e2e-verified
(infra-manifest v2, ADR 0017): apps and services carry
`staging`/`production` labels; per-env manifest overlays
(`deploymate.{env}.yml` replaces the base services list); service
resolution and URL injection are environment-scoped (`staging-postgres`
vs `postgres`, separate volumes); manifest entries accept pins
(`postgres:17`) and **no tag means `latest`** (owner's rule —
`postgres:latest` caveat: Postgres volumes are major-version-locked, so
pin in production). `POST /apps/{slug}/environment` + env badges in the
UI. E2e verified: both envs in one project with isolated services and
URLs, overlay replacement, latest + pinned images, idempotent redeploys.
Spec: `docs/specs/infra-manifest.md`. Also earlier the same day: infra
manifest v1 (commit `58ae4ec`, pushed) and the progress/CLAUDE.md
handoff files (`abcbbb2`).

**Commits not yet pushed:** `da76b0b`, `abcbbb2`, plus the environments
commit. Push with the bjboss007 rule below.

**Verified state:** everything shipped so far is e2e-verified on macOS/
Docker Desktop. **NOT verified:** real Ubuntu server `bootstrap.sh` run,
real Let's Encrypt issuance, auto-DNS for preview hostnames (needs a server).

## Next up (ordered)

1. **Push the local commits** (`gh auth switch --user bjboss007` first).
2. **Real e2e suite** — `testdata/e2e.sh` covers only the P2 smoke path;
   everything else is manually verified per feature. Entry: `testdata/e2e.sh`,
   the verification sections in `docs/specs/*.md`, backlog item in
   `docs/improvements.md` (Near-term).
3. **Preview hostnames go live** — per-app Cloudflare DNS records are
   manual today; auto-DNS via the Cloudflare API is the missing piece.
   Entry: `docs/specs/cloudflare-tunnel.md`, `internal/httpserver/server.go`
   (previewHost routing), backlog item (Near-term).
4. **Deployment command/timeout** — manual deploys run in the HTTP handler
   with no timeout; move them onto the worker queue. Entry:
   `internal/httpserver/handlers_apps.go` (`handleAppDeploy`),
   `internal/jobs/worker.go`, backlog item (Near-term).
5. Anything else: the full ordered backlog is `docs/improvements.md`.

**Bigger milestone on the horizon:** first real-server run
(`deploy/bootstrap.sh` on Ubuntu 24.04, production LE certs) —
`docs/knowledge/server-setup.md`, `deploy/bootstrap.sh`.

## Before claiming something works

- `make test` and `make vet` must pass.
- A feature is "done" only when **e2e-verified** against a running server
  (manual verification is the norm until the real e2e suite exists). The
  throwaway pattern used for the manifest verification: fresh server on a
  spare port + scratch data dir + local git repos, clean up after.
- If you notice a gap while working: add it to `docs/improvements.md`
  **first**, fix it with its own change later.

## Reading path (pointers, not scans)

- Whole system on one page: `docs/knowledge/architecture.md`
- The heart of the product: `docs/knowledge/deploy-flow.md`
- Dev environment quirks (port 8090, Docker Desktop socket, SDK pins,
  templ path, railpack): `docs/knowledge/dev-environment.md`
- Decisions: `docs/decisions/` — 0001 first; 0013 Railpack, 0014 alerts,
  0015 resources/port mapping, 0016 env aliasing
- Specs: `docs/specs/` — alerts, cloudflare-tunnel, infra-manifest
- Database semantics: `docs/knowledge/database.md` ·
  security: `docs/knowledge/security.md` ·
  troubleshooting: `docs/knowledge/troubleshooting.md`
- Backlog: `docs/improvements.md`

## Hard-won rules (skipping these burns an hour)

- **Before every push:** `gh auth switch --user bjboss007`, then verify
  `gh auth status` shows it active — the account flips back to
  `habibmuhammad002` on its own, and pushes fail with "Repository not
  found". Remote: `github.com/bjboss007/deploymate` (HTTPS).
- **Manifest versions:** entries without a version run `latest` (owner's
  rule). Postgres volumes are major-version-locked — `postgres:latest`
  bumping majors can refuse to start on an old volume; pinning is the
  documented escape hatch.
- **templ literals:** templ treats `{...}` as expressions — literal
  braces in text need a string expression (`{ "deploymate.{env}.yml" }`),
  and `\{` is illegal. `make gen` regenerates; commit `_templ.go` too.
- Docs live in `docs/` — ADRs, knowledge, specs, improvements. Update them
  with the code, not later.
- Never silently fix a gap in scope — backlog it first (see above).

## Update protocol (before stopping, every session)

1. Rewrite "Where we stopped" (date, what shipped, commit, what's
   unverified) and re-order "Next up".
2. Add any new quirk/rule you discovered.
3. Commit `progress.md` together with the work and push (bjboss007 rule).
