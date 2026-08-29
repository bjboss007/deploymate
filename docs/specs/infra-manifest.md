# Infrastructure manifest (`deploymate.yml`) — specification

**Status:** live (Aug 2026) · Environments + image pins shipped in the
v2 update (ADR 0017). Implementation notes at the bottom.

## Problem

Wiring is automatic but provisioning used to be manual: an app that needs
Postgres gets `DATABASE_URL` injected *once a service is running* — but
nothing created the service. The goal: **push a repo, its infrastructure
comes to life.** A Rails repo with a manifest declaring Postgres + Redis
boots with both provisioned, URLs injected, and aliases resolved — zero
clicks. v2 adds environments: staging and production each get their own
services, selected by a per-environment overlay.

## Design

### The manifest

`deploymate.yml` at the repo root (or the app's `root_directory`):

```yaml
services:
  - postgres
  - redis:7-alpine
```

- **Entries are `type` or `type:tag`.** No tag means the **latest**
  image of that type (`postgres` → `postgres:latest`) — pins exist
  precisely to pin; `postgres:17` runs exactly 17. *Caveat:
  `postgres:latest` is a floating major version and Postgres data
  volumes are major-version-locked — pin in production.*
- Duplicate entries collapse; the same type declared with *different*
  pins is a deploy error (a silently dropped pin would be
  non-deterministic).
- File absent → no-op (the no-manifest behavior).
- Malformed YAML or unknown type → **deploy fails with a clear error**
  in the build log. Declarative config must be deterministic, never
  silently ignored.

### Environment-specific manifests

An app has an `environment` (production | staging, set on the app page).
Besides the base file, the repo may carry an overlay per environment:

```yaml
# deploymate.staging.yml — REPLACES the base services list for staging
services:
  - postgres:16-alpine
```

- `deploymate.yml` + `deploymate.{env}.yml` are read together; the
  overlay's `services` list **replaces** the base's entirely when the
  file exists (docker-compose list semantics — staging can drop
  production's Redis, or pin different images). An empty overlay file
  means "no declared services". Either file may exist without the other.
- Production also honors `deploymate.production.yml` (uniform rule).

### Resolution & diff (at every deploy, before container create)

1. Parse the manifest for the app's environment.
2. For each declared type, find an existing service **of that type in
   the app's environment** (any name — `Main DB` satisfies `postgres`).
   Services in other environments are invisible and never touched.
   - Found and running → reuse (URLs inject as usual).
   - Found but stopped → start it (full readiness wait).
   - Not found → **create + provision automatically**: production gets
     name/slug `postgres`; staging gets name `Staging PostgreSQL`, slug
     `staging-postgres`, its own volume (`dm-svc-staging-postgres-data`).
     Generated credentials, volume, readiness.
3. Services not declared are **never touched** — no auto-delete, ever.
   Deleting infra is a human decision. Switching an app's environment
   leaves the old environment's services running untouched.
4. Multiple apps in one environment declaring the same type converge on
   the same service (one Postgres per environment unless the user names
   more).

### Where it runs

In the worker's deploy path, after clone and before the build — so
provisioning overlaps the image build and the new URLs are present in
the very env assembly that injects them. Service provisioning lives in
the shared `ServiceProvisioner` (used by handlers *and* the worker).

### Events & observability

- Build log lines: `manifest: postgres, redis` (staging:
  `manifest (staging): postgres`), `manifest: reusing existing postgres
  service`, `manifest: provisioning postgres (new service,
  postgres:17)`.
- Events (history timeline): `service_auto_provisioned`,
  `service_started`, `environment_changed`.
- Failures fail the deployment — visible in the deployment page, not a
  background surprise.

## Security

The manifest ships with the user's own code and has the same trust level
as the Dockerfile/Railpack build — it cannot do anything the build
couldn't already do. Auto-provisioned services follow the exact same
credential/encryption/network rules as manual ones. Image tags are
unverifiable at parse time; a bad tag fails at pull time with the docker
error in the deploy log.

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
6. *(v2)* Unpinned entries run `{type}:latest`; pinned entries run the
   pinned image.
7. *(v2)* Staging app + production app in one project: each gets its own
   Postgres (`staging-postgres` vs `postgres`), each app's env carries
   only its own URLs, and a `deploymate.staging.yml` overlay replaces
   the base list (a dropped service is not provisioned).

## Out of scope (v2+)

- Auto-delete/teardown of unused services (v1/v2 never deletes —
  manifest-created services accumulate)
- Database seeding/backups from the manifest

## Implementation notes (Aug 2026)

- **`ServiceProvisioner`** (`internal/services/provisioner.go`): the
  provisioning logic shared by handlers and the worker — `Provision`
  (creds → image → container → readiness → status), `Restart`, and
  `Ensure` (the environment-scoped resolution/diff loop). `Provision`
  runs the service row's stored `image` (pin or latest) rather than the
  template default.
- **Parsing** (`internal/services/manifest.go`): `ParseManifest(name,
  data)` + `LoadManifest(checkoutDir, rootDir, env)` (base + env
  overlay). Unknown types, bad pins, duplicate-type-different-pins, and
  malformed YAML are errors; unknown top-level keys are ignored (forward
  compat).
- **Schema** (migration 0009): `apps.environment` +
  `services.environment`, default production; `AppEnv` filters injected
  URLs by the app's environment. `POST /apps/{slug}/environment` flips
  an app; `environment_changed` event records it.
- **Worker hook** (`internal/jobs/worker.go`): after clone and before
  the build. Rollback/resize deployments do not resolve the manifest
  (no checkout) — they reuse whatever services already exist via env
  assembly.
- **Tests**: parser + provisioner env-scoping + `AppEnv` filtering unit
  tests; e2e verification covered items 6-7 above.
