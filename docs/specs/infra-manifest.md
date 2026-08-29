# Infrastructure manifest (`deploymate.yml`) — specification

**Status:** live (Aug 2026) · Implementation notes at the bottom.

## Problem

Today, wiring is automatic but provisioning is manual: an app that needs
Postgres gets `DATABASE_URL` injected *once a service is running* — but
nothing creates the service. The goal: **push a repo, its infrastructure
comes to life.** A Rails repo with a manifest declaring Postgres + Redis
should boot with both provisioned, URLs injected, and aliases resolved —
zero clicks.

## Design

### The manifest

`deploymate.yml` at the repo root (or the app's `root_directory`):

```yaml
services:
  - postgres
  - redis
```

- **Types only in v1** — no image pins, no sizing (resource detection
  already handles sizing; image pinning is a v2 extension point).
- File absent → no-op (today's behavior).
- Malformed YAML or unknown type → **deploy fails with a clear error** in
  the build log. Declarative config must be deterministic, never
  silently ignored.

### Resolution & diff (at every deploy, before container create)

1. Parse the manifest from the checkout.
2. For each declared type, find an existing service **of that type** in
   the app's project (any name — `Main DB` satisfies `postgres`).
   - Found and running → reuse (URLs inject as usual).
   - Found but stopped → start it (full readiness wait).
   - Not found → **create + provision automatically**: name = the type
     (`postgres`, `redis`), generated credentials, volume, readiness.
3. Services not declared are **never touched** — no auto-delete, ever.
   Deleting infra is a human decision.
4. Multiple apps in one project declaring the same type converge on the
   same service (one Postgres per project unless the user names more).

### Where it runs

In the worker's deploy path, after clone and before `runContainer` — so
the new URLs are present in the very env assembly that injects them.
This requires extracting service provisioning out of the HTTP handler
into a shared `ServiceProvisioner` (used by handlers *and* the worker) —
the main refactor of this feature.

### Events & observability

- Build log lines: `manifest: provisioning postgres (new service)`,
  `manifest: reusing existing postgres service`.
- Events (history timeline): `service_auto_provisioned`.
- Failures fail the deployment — visible in the deployment page, not a
  background surprise.

## Security

The manifest ships with the user's own code and has the same trust level
as the Dockerfile/Railpack build — it cannot do anything the build
couldn't already do. Auto-provisioned services follow the exact same
credential/encryption/network rules as manual ones.

## Verification

Fixture repo `testdata/repos/manifest-app` (Node, `deploymate.yml`
declaring postgres):

1. Deploy → postgres service auto-created, provisioned, `DATABASE_URL`
   injected; app boots and queries.
2. Redeploy → idempotent: same service reused, no duplicates, no
   credential regeneration.
3. Unknown type (`mongo`) → deploy fails with a clear error.
4. Malformed YAML → deploy fails with the parse error.
5. Existing user-created postgres service in the project → reused, not
   shadowed.

## Out of scope (v2+)

- Image/version pins per service in the manifest
- Auto-delete/teardown of unused services
- Environment-specific manifests (`deploymate.staging.yml`)
- Database seeding/backups from the manifest

## Implementation notes (Aug 2026)

- **`ServiceProvisioner`** (`internal/services/provisioner.go`): the
  provisioning logic extracted out of the HTTP handlers — `Provision`
  (creds → image → container → readiness → status), `Restart`, and
  `Ensure` (the resolution/diff loop). Handlers and the worker share one
  instance, so "a human clicked Start" and "a manifest declared postgres"
  are the same code path.
- **Parsing** (`internal/services/manifest.go`): `ParseManifest` +
  `LoadManifest(checkoutDir, rootDir)` — unknown types and malformed YAML
  are errors; unknown top-level keys are ignored (forward compat);
  duplicates collapse; a missing file is a no-op.
- **Worker hook** (`internal/jobs/worker.go`): after clone and before the
  build, so provisioning runs while the image builds and the URLs are
  present in the env assembly that injects them. Build-log lines are
  `manifest: reusing existing postgres service`,
  `manifest: starting existing postgres service`, and
  `manifest: provisioning postgres (new service)`; history events are
  `service_auto_provisioned` (new) and `service_started` (resumed).
  Rollback/resize deployments do not resolve the manifest (no checkout) —
  they reuse whatever services already exist via env assembly.
- **Convergence**: one service per type unless the user names more;
  resolution prefers a running service when several of the type exist.
  Services are never auto-deleted; `deploymate.yml` failures fail the
  deployment with the error in the deployment page.
- **Tests**: table-driven parser tests + `Ensure` resolution tests with a
  fake runtime (`internal/services/*_test.go`); fixture repo
  `testdata/repos/manifest-app` (Node + pg, queries the injected
  `DATABASE_URL`).
