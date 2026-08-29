# Progress — DeployMate

> **Handoff file.** First thing any agent reads; last thing updated before
> stopping. Read this whole file (~1 minute), then follow the pointers for
> the task at hand — do **not** scan the repo to orient. If a pointer below
> doesn't answer something, read `docs/knowledge/architecture.md` next.

## Where we stopped

**2026-08-29** — Infra manifest feature shipped and e2e-verified: repos can
declare `deploymate.yml` (`services: [postgres, redis]`); every git deploy
reconciles the project's services with the declaration (reuse / start /
auto-provision) before the app container is assembled. Provisioning was
extracted into a shared `ServiceProvisioner` (`internal/services/provisioner.go`)
used by handlers and the worker; parser in `internal/services/manifest.go`;
unit tests in `internal/services/*_test.go`; fixture
`testdata/repos/manifest-app`. All five spec checks passed against real
Docker/Postgres. Commit `58ae4ec` (pushed). Spec:
`docs/specs/infra-manifest.md`.

**Commits not yet pushed:** `da76b0b` (backlog entry for manifest v2) and
the commit adding this file. Push with the bjboss007 rule below.

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
- Docs live in `docs/` — ADRs, knowledge, specs, improvements. Update them
  with the code, not later.
- Never silently fix a gap in scope — backlog it first (see above).

## Update protocol (before stopping, every session)

1. Rewrite "Where we stopped" (date, what shipped, commit, what's
   unverified) and re-order "Next up".
2. Add any new quirk/rule you discovered.
3. Commit `progress.md` together with the work and push (bjboss007 rule).
