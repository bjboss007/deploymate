# 0017 — Environment scoping: dev/staging/production labels on apps and services

- **Date:** 2026-08-29 (dev environment added 2026-08-30)
- **Status:** accepted

## Context

Apps need staging and production with *different backing infrastructure* —
a staging deploy should hit a staging database, not the production one.
The natural reading of "environment" is separate running instances of the
app, but that is a much bigger model change (multiple containers, domains,
and env-var sets per app). The infra-manifest v2 work only needed the
environment to select *which services and manifest apply*.

## Decision

- **Environment is an app-level label** (`apps.environment` and
  `services.environment`, both `TEXT NOT NULL DEFAULT 'production'`).
  One container per app — unchanged. The label selects the manifest
  overlay and the service set.
- **Per-env manifests**: `deploymate.yml` plus an optional
  `deploymate.{env}.yml` overlay. The overlay's `services` list
  **replaces** the base's entirely when present (docker-compose list
  semantics; an empty overlay means no declared services).
- **Per-env services**: resolution (`Ensure`) and URL injection
  (`AppEnv`) filter by environment. Auto-provisioned non-production
  services get the env-prefixed slug `{env}-{type}` (global slug
  uniqueness makes collisions impossible with production's `{type}`),
  name `{Env} {Label}` (e.g. `Dev PostgreSQL`, `Staging PostgreSQL`),
  and their own volume (`dm-svc-dev-postgres-data`).
- **Image pins with latest default**: manifest entries are `type` or
  `type:tag`; no tag means `{type}:latest`. The service row's `image`
  column (previously write-only metadata) is now what `Provision` runs.
- Manual services default to production (the create form has no env
  selector); the manifest is the way to obtain dev/staging services.
- Existing rows migrate to production — zero behavior change for
  existing deployments.

### Addendum (2026-08-30): the `dev` environment

A third label, `dev`, was added alongside staging and production, and it
is now the **default environment for newly-created apps** (`store.EnvDev`
in `store.CreateApp`). It behaves exactly like staging — its own
env-prefixed services (`dev-postgres`, name `Dev PostgreSQL`, volume
`dm-svc-dev-postgres-data`), its own `deploymate.dev.yml` overlay, and
full isolation from the other environments. The service-naming label was
generalized from the hardcoded `Staging ` prefix to a capitalized env
name (`strings.ToUpper(env[:1]) + env[1:]`) so any non-production
environment names itself. Migration `0009`'s column
default stays `'production'` (only a fallback the store overrides); no new
migration was needed since the column carries no CHECK constraint.

## Consequences

- Switching an app's environment never touches the old environment's
  services or data; the next deploy provisions the new set.
- `postgres:latest` is a floating major version and Postgres data
  volumes are major-version-locked — pinning in the manifest is the
  documented escape hatch.
- Staging service slugs are global: a manual service named
  `staging-postgres` is reused by staging manifests, but a *production*
  service squatting on the slug fails staging deploys with a
  rename-or-delete error.
- Verified e2e: staging and production apps in one project each got
  their own Postgres with correct URLs, overlays replaced base lists,
  pins and latest both honored, no cross-env leakage, redeploys
  idempotent.

## Alternatives

- **Full multi-instance apps** (per-environment containers, domains,
  env vars, deploy targets) — the real "per-branch deploys" feature;
  deferred as its own backlog item. This decision's columns and naming
  compose with it later.
- **Separate projects per environment** — works today by convention but
  relies on the human to keep the pair consistent; the label makes the
  relationship data.
- **Env prefix on container names only** (no services column) — the
  services table is where isolation actually matters (URLs, volumes);
  naming alone would not isolate anything.
- **Overlay merges/union** — can't express "staging drops production's
  Redis"; replace is deterministic and composable (repeat the base
  entries you keep).
