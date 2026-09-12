# App replicas + load balancing — specification

**Status:** design (Sep 2026) — decisions locked with the owner, **not
implemented**. Backlog: "App replicas (horizontal scaling)" in
`docs/improvements.md`. Related: `docs/knowledge/architecture.md`,
`internal/appspec`, `internal/proxy`, `internal/swap`.

## Problem

An app is exactly one container. There is no way to run two, so there is
no throughput scaling and no process-level redundancy: one OOM, one leaked
goroutine, one crash-looping boot takes the app down. Deploys get a
blue/green swap (zero-downtime), but steady-state load never spreads.

This is **Axis 1** of scaling — horizontal replicas of stateless apps on
one node. Axis 2 (multi-node) stays out of scope; the `runtime.Runtime`
seam remains its preparation.

## Why this is cheap here (owner's insight)

DeployMate's model already splits compute from state: databases and caches
are separate service containers (`dm-svc-<slug>`), and apps connect to
them over the docker network via injected `DATABASE_URL`/`MYSQL_URL`/
`REDIS_URL`. **App containers have no volumes at all** — every deploy
recreates them. So replicas of an app are data-safe by construction; the
shared Postgres/Redis is exactly the 12-factor shape. Nothing about
volumes or databases needs solving for this feature.

## Owner decisions (Sep 2026)

1. **Health: any replica up = the app is up** — and the LB must be
   intelligent enough not to route to a sick/dead replica.
2. **Logs: merged** across replicas, with the ability to drill into one
   replica's stream when tracing.
3. **Replica count: capped** at a sane maximum (proposed: **5**).
4. **Rollout floor: at least one replica serving at all times.**

## Design

### Model

- `apps.replicas` — integer, default **1**, min 1, max **5** (cap is a
  constant; a typo must never spawn 100 containers).
- New table `app_replicas` (migration **0015**), one row per running slot:
  `id, app_id, slot (1..N), container_name, host_port, status, deploy_id,
  updated_at`, UNIQUE(app_id, slot).

  Slot containers are named `dm-{slug}-r{slot}`; a rollout stages
  `dm-{slug}-r{slot}-{deployID}` beside the old one, exactly like today's
  single-container swap. `apps.preview_host_port` (migration 0012) stays
  and keeps mirroring slot 1's port — the fallback path for older readers.
- `runtime.PreviewPort(slug)` grows a slot-aware sibling
  (`PreviewPortFor(slug, slot)`) so each slot has a deterministic loopback
  port; the replica table stores what a swap actually recorded, as today.

### Routing (the LB)

One address per app, replicas behind it — whichever LB owns the path picks
the replica.

- **Traefik path (public subdomains; the real one on the Linux box):**
  `proxy.AppLabels` changes so every replica declares the **same service
  name** (`traefik.http.services.{slug}.loadbalancer.server.port`) and the
  same router rule; Traefik merges the servers of a shared service name
  into one backend and round-robins. Exactly one logical router per app —
  the router block is emitted identically by all slots.
- **Sick-replica removal (decision 1):** the shared service carries
  Traefik **active healthcheck** labels —
  `traefik.http.services.{slug}.loadbalancer.healthcheck.path=/`,
  `.interval=10s`, `.timeout=3s`. Traefik pulls a failing server out of
  rotation until it recovers; requests never land on a dead replica.
  (Default path `/` matches what the monitor already probes; a per-app
  health path is a follow-up, then both use it.)
- **Dashboard `/preview/{slug}` path (the only path on macOS dev):** the
  in-process proxy round-robins across the app's replica ports from the
  table, skips slots the monitor last saw unhealthy, and retries the next
  replica on a dial error. One preview URL, LB behind it.

### Rollout choreography (decision 4)

Scaling **up** (e.g. 2 → 3) starts the new slot(s) with the *current*
deployment's image/spec — no rebuild, no swap of existing slots.
Scaling **down** stops/removes the highest slots. Both are immediate,
no-downtime.

A **deploy** (new image) rolls slot by slot, never removing an old
container before its replacement is healthy:

1. Build once (one image, as today).
2. For each slot i: stage `dm-{slug}-r{i}-{deployID}` **joined to the
   shared Traefik service** (no priority trick — the healthcheck keeps it
   out of rotation until it passes), probe it (30×2s, as today), remove
   and rename to `dm-{slug}-r{i}`, update the replica row.
3. Floor: at least one replica serves throughout — replacing one at a
   time on N ≥ 2 always leaves ≥ N-1 ≥ 1 up; N = 1 keeps today's existing
   swap untouched.

Note the deliberate divergence: N = 1 keeps the current per-container
router + UnixNano priority swap (no regression); N ≥ 2 uses the shared
service + healthcheck rotation. Unifying them is a later cleanup.

**Failed rollout:** halt, mark the deployment failed, alert. Slots already
replaced stay on the new image, the rest on the old; the app keeps
serving (floor held). A redeploy converges. Documented boundary — no
automatic rollback of already-replaced slots.

### Health, status, healing

- The monitor probes **each slot** (ports from the replica table).
  App-level `apps.health` = healthy when **any** slot answers (decision
  1). Per-slot failures drive the existing heal path per container
  (recreate/restart that slot), rate-limited as today.
- `apps.status` aggregates: running when ≥ 1 slot runs; the deployment
  row stays per-app as today.
- `/stats` and resize: limits are per container — the UI shows
  `limit × replicas` as the app's total ask; resize applies to all slots
  (a resize is a rollout).

### Logs (decision 2)

- `handleAppLogs` fans in all slots' docker log streams into one SSE,
  lines prefixed `[r1] …`; `?replica=r2` narrows to one slot (the panel
  gets a small replica selector). A stopped/removed slot keeps the
  existing stateless-tail behavior per container.
- Build logs are untouched (they belong to the deployment, not a slot).

### App-author notes (documented in the UI footer + database.md)

Replicas don't add a statefulness requirement — apps are already
ephemeral — but three classes of app-level behavior need a line:

- **Migrations:** Flyway/Liquibase/Prisma keep a history table and most
  take a lock, so concurrent boots serialize or the loser fails fast and
  the restart policy closes the loop (self-healing). Caveat: engines
  without transactional DDL (**MySQL**) can leave a partially-applied
  migration if a runner dies mid-way. Postgres (the default) fails
  cleanly. No platform hook — this is a documented app-author concern.
- **Singleton loops:** an app starting a background worker/cron on boot
  multiplies it ×N. Run it in one place or make it idempotent.
- **Local filesystem:** writes to the container FS are already lost on
  redeploy; with replicas they become *intermittent* (served by the wrong
  replica). Uploads/sessions belong in a service (Redis) or the DB.

## Verification

Unit: label merging (shared service name, identical router across slots,
healthcheck labels present), slot naming + port derivation, replica cap
validation, proxy round-robin + skip-unhealthy + failover, health
aggregation (any-up), rollout ordering (never remove before healthy),
scale up/down math.

E2e (extend `testdata/e2e_manual.sh` or a new `make e2e-replicas`,
throwaway server):

1. Deploy `replicas=2` → both slot containers up, both on the shared
   Traefik service (assert labels), /preview round-robins (hit N times,
   observe both containers via a distinguishing response or a
   `docker exec hostname` compare).
2. Kill one slot's container → app health stays healthy (any-up), the
   preview proxy stops dialing the dead port, heal restarts it.
3. Deploy a new image → sample /preview through the rollout, never a
   non-200 (floor ≥ 1).
4. Merged logs show lines from both slots with prefixes; `?replica=`
   narrows.
5. `replicas=2 → 3` starts one new slot with the same image (no rebuild
   event); `3 → 1` removes the extras.
6. Cap: `replicas=6` rejected at save.

## Out of scope

- Autoscaling (manual count only), sticky sessions, per-replica resource
  tuning.
- Multi-node placement (Axis 2: `deploymate agent` over gRPC + pinning).
- Stateful replication / DB HA — managed services stay single-node
  appliances; replicas share one service.
