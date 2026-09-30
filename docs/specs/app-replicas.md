# App replicas + load balancing — specification

**Status:** **implemented 2026-09-30** (ADR 0018). Decisions locked with
the owner, one adversarial review pass, and the two blocking Traefik
spikes were run first — their results changed the label scheme and a few
mechanics; see **Spike results** and **As built** at the end. Where this
document and ADR 0018 disagree, the ADR wins. Backlog: "App replicas
(horizontal scaling)" in `docs/improvements.md`. Related:
`docs/knowledge/architecture.md`, `internal/appspec`, `internal/proxy`,
`internal/swap`.

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

## Blocking spikes (on Linux, before building)

The label scheme below assumes Traefik behaviors we have **not** verified
(Traefik cannot be exercised on Docker Desktop — see dev-environment.md).
Do these two experiments on a Linux box with real Traefik first; record
results here and lock the scheme before writing feature code:

1. **Merge semantics.** N containers declaring (a) the same
   `traefik.http.services.{slug}.loadbalancer.server.port` and (b) the
   identical `traefik.http.routers.{slug}.*` block — does Traefik merge
   them into one service round-robining N servers, or does the duplicate
   router conflict? Determine whether the service-only + one-router-owner
   pattern is required. Fallback plan if routers must be owned once:
   **slot 1 carries the router labels; every slot carries the service
   labels** (a renamed container keeps its labels, and scale-down removes
   highest slots, so slot 1 always exists — the router ownership is
   stable).
2. **Healthcheck behavior.** (a) A newly started server's initial state —
   if it is "healthy" before the first check, a staged replica receives
   traffic *before* our 30×2s probe passes (see the rollout honesty note
   below); (b) what Traefik does when **all** servers of a service fail
   their healthcheck (503? route anyway?) and whether it recovers
   automatically when one passes again; (c) do duplicate healthcheck
   labels on the shared service merge cleanly.

Output of the spikes: a short "Spike results" addendum here, and — if the
fallback label pattern is needed — that becomes the design of record.

## Design

### Model

- `apps.replicas` — integer, default **1**, min 1, max **5** (cap is a
  constant; a typo must never spawn 100 containers).
- `apps.health_path` — text, default `/`. Now **load-bearing** (v1, not a
  follow-up — see the failure mode below): it feeds BOTH the Traefik
  active healthcheck and the monitor probe, so they agree on what
  "serving" means. A wrong path ejects every replica at once — the LB
  would cause the outage it exists to prevent. Apps must serve it.
- New table `app_replicas` (migration **0015**), one row per running slot:
  `id, app_id, slot (1..N), container_name, host_port, status, deploy_id,
  updated_at`, UNIQUE(app_id, slot).

  Slot containers are named `dm-{slug}-r{slot}`; a rollout stages
  `dm-{slug}-r{slot}-{deployID}` beside the old one, exactly like today's
  single-container swap.
- **Preview ports become probe-allocated, not hashed-only.**
  `runtime.PreviewPortFor(slug, slot)` derives the same deterministic
  candidates as today, and the allocator linear-probes (up to ~50 ports)
  against the ports in use (replica table + apps fallback) so a hash
  collision — a pre-existing gap, multiplied by ×5 — can't fail a create.
  The replica table stores what a swap actually recorded.
- `apps.preview_host_port` becomes **deprecated compatibility**: after
  the change the replica table is the single source of truth; the column
  is dual-written with slot 1's port for **one release** so a binary
  rollback can still resolve previews, then removed. Readers
  (monitor, proxy) move to the table in the same change.

### Routing (the LB)

One address per app, replicas behind it — whichever LB owns the path picks
the replica.

- **Traefik path (public subdomains; the real one on the Linux box):**
  `proxy.AppLabels` changes so every replica declares the **same service
  name** and (pending spike 1) the same router rule or the
  slot-1-owns-router fallback; Traefik merges the servers of a shared
  service name into one backend and round-robins.
- **Sick-replica removal (decision 1):** the shared service carries
  Traefik **active healthcheck** labels —
  `traefik.http.services.{slug}.loadbalancer.healthcheck.path=` +
  `apps.health_path`, `.interval=10s`, `.timeout=3s`. Traefik pulls a
  failing server out of rotation until it recovers. Behavior when all
  servers are unhealthy is **per spike 2b** — if Traefik 503s, that is
  acceptable (the app genuinely is down) but must be documented, and
  recovery must be automatic.
- **Dashboard `/preview/{slug}` path (the only path on macOS dev):** the
  in-process proxy round-robins across the app's replica ports from the
  table, skips slots the monitor last saw unhealthy, and retries the next
  replica on a dial error. One preview URL, LB behind it.

### Rollout choreography (decision 4)

Scaling **up** (e.g. 2 → 3) starts the new slot(s) with the *current*
deployment's image (`deployments.image_tag` — note: git apps have an
empty `app.Image`, the built tag lives on the deployment row) plus the
app row's current env/limits/entrypoint. Recorded as a deployment row
(`kind = 'scale'`, no build) so the history and each slot's `deploy_id`
stay coherent. Documented skew: if the app row's env changed since the
current deployment, the new slot boots with the *newer* env while old
slots still run the old one — a redeploy converges. Scaling **down**
stops/removes the highest slots, same deployment-row treatment. Both are
no-downtime (no graceful connection drain — see out of scope).

A **deploy** (new image) rolls slot by slot, never removing an old
container before its replacement is healthy:

1. Build once (one image, as today).
2. For each slot i: stage `dm-{slug}-r{i}-{deployID}` **joined to the
   shared Traefik service** (no priority trick — see honesty note),
   probe it (30×2s, as today), remove and rename to `dm-{slug}-r{i}`,
   update the replica row.
3. Floor: at least one replica serves throughout — replacing one at a
   time on N ≥ 2 always leaves ≥ N-1 ≥ 1 up; N = 1 keeps today's existing
   swap untouched.

**Rollout honesty (N ≥ 2).** Because a staged slot joins the shared
service immediately, the "prove before serve" guarantee of the N=1 swap
weakens to **healthcheck-mediated**: the new slot can receive traffic
before our probe passes, bounded by Traefik's healthcheck interval and
its initial-server state (spike 2a). A crash-looping new slot can
therefore cause a few intermittent 5xx during its rollout — the floor
(≥1 old replica) still holds throughout. If spike 2a shows servers start
healthy and this window is judged unacceptable, the alternative is
staging the slot *without* service labels and adding them post-probe —
which requires recreating the container (labels are immutable) — costed
before adopting.

**Failed rollout:** halt, mark the deployment failed, alert. Slots already
replaced stay on the new image, the rest on the old — the app keeps
serving (floor held). **Drift is surfaced, not hidden:** each replica row
carries its `deploy_id`; the app page shows a per-slot deploy badge and a
"2/3 on newer image" note when slots disagree, and `current_deployment_id`
is shown as the *target*. Rollback converges every slot to the old image.
No automatic rollback of already-replaced slots — documented boundary.

### Health, status, healing

- The monitor probes **each slot** (ports from the replica table, path
  from `apps.health_path`). App-level `apps.health` = healthy when **any**
  slot answers (decision 1); the badge shows the ratio when degraded
  ("running 2/3") so the any-up rule isn't hiding dead replicas.
- Per-slot failures drive the existing heal path per container
  (recreate/restart that slot), rate-limited as today.
- **Heal/rollout mutex:** the monitor never heals an app whose deployment
  is queued/building — mid-rollout the "down" slot is being replaced on
  purpose, and healing it would fight the rollout (duplicate containers,
  churn). Heal resumes once the deployment is terminal.
- `apps.status` aggregates: running when ≥ 1 slot runs; the deployment
  row stays per-app as today.
- `/stats` and resize: limits are per container — the UI shows
  `limit × replicas` as the app's total ask; resize applies to all slots
  (a resize is a rollout).

### Logs (decision 2)

- `handleAppLogs` fans in all slots' docker log streams into one SSE,
  lines prefixed `[r1] …`; `?replica=r2` narrows to one slot (a filter in
  the panel, not a second panel). A stopped/removed slot keeps the
  existing stateless-tail behavior per container.
- Build logs are untouched (they belong to the deployment, not a slot).

### Touchpoints (complete list — nothing else changes)

- `internal/migrations/0015`: `apps.replicas`, `apps.health_path`,
  `app_replicas`.
- `internal/store`: replica row CRUD; app-row reads grow the two columns.
- `internal/appspec`: slot naming (`dm-{slug}-r{i}` / staged variant),
  port allocation calls, spec build unchanged otherwise.
- `internal/proxy`: shared-service label scheme (+ healthcheck labels,
  router ownership per spike 1).
- `internal/swap`: generalized to rolling per-slot swaps; N=1 path kept
  as-is (named debt — see follow-ups).
- `internal/jobs/worker`: build once, roll slots; scale up/down
  deployment rows (`kind='scale'`).
- `internal/httpserver/handlers_apps.go`: start/stop/restart/delete/resize
  fan out to all slots; delete removes N containers; logs fan-in
  (`handlers_logs.go`); app page replicas control + slot badges.
- `internal/monitor`: per-slot probes, any-up aggregation, heal gated by
  in-flight deployments.
- `internal/httpserver/handlers_preview.go` + `internal/proxy`: round-robin
  over the replica table with skip-unhealthy + failover.
- Untouched: backups (service-scoped), services, DNS, alerts.

## Follow-ups / named debt

- **Unify the N=1 and N≥2 swap paths** once the spikes land — two
  mechanisms in the riskiest subsystem will drift; the floor-≥1 test must
  cover both until then.
- **Per-app health path UI**: the field ships in v1 (default `/`); a
  form control for it can follow once an app needs a non-root path.
- **Connection drain** (see out of scope) if in-flight resets prove to
  matter in practice.

## Verification

Unit: label merging (shared service name, router per spike outcome,
healthcheck labels present, health path from the app row), slot naming +
probe-allocated ports (incl. collision advance), replica cap validation,
proxy round-robin + skip-unhealthy + failover, health aggregation
(any-up) + degraded ratio, rollout ordering (never remove before
healthy), heal/rollout mutex, scale up/down math + `scale` rows.

E2e (extend `testdata/e2e_manual.sh` or new `make e2e-replicas`,
throwaway server, macOS): **Traefik itself cannot run here**, so the e2e
asserts what it can — both slot containers up with the shared service
labels + healthcheck labels present (container inspect), /preview
round-robins across slots (distinguish via a per-container response),
proxy skips a killed slot and heal restarts it, merged logs with prefixes
+ `?replica=` filter, scale 2→3→1 (+ `scale` deployment rows), cap
rejected, and the *label presence* for healthcheck path. Actual LB
ejection/recovery semantics are **server-verified** (spike 2 doubles as
the harness) — this is a milestone dependency, recorded here so the
server run owns it.

## Out of scope

- Autoscaling (manual count only), sticky sessions, per-replica resource
  tuning.
- Multi-node placement (Axis 2: `deploymate agent` over gRPC + pinning).
- Stateful replication / DB HA — managed services stay single-node
  appliances; replicas share one service.
- Graceful connection drain on slot removal (pre-existing: the N=1 swap
  force-removes after flip; replicas multiply the surface but don't
  introduce it — revisit per follow-ups).

## Spike results (2026-09-30)

Run against **Traefik v3.3** (the `deploy/bootstrap.sh` pin) with the
docker provider. Docker Desktop *can* host this after all: mounting the
VM path `-v /var/run/docker.sock:/var/run/docker.sock` works (only the
host-side `~/.docker/run/docker.sock` path cannot be mounted — the
dev-environment.md limitation). Test servers: `traefik/whoami` (echoes
its hostname; `POST /health` flips its health status) and `nginx:alpine`.

1. **Merge semantics.**
   - (a) N containers with **identical** router + service labels → one
     router, one service with N servers, round-robin (10/10 over 20).
   - (b) Two containers defining the **same router name with a different
     rule** (a domain edited mid-rollout) → Traefik **drops the router;
     every request 404s.**
   - (c) Slot-1-owns-router fallback → works, but each service-only
     container gets an auto-generated router `Host(<container-name>)`.
   - (d) **Same service name, different healthcheck path** → the service
     is **dropped; every request 404s.** Service labels must be
     byte-identical wherever a name is shared.
   - (e) **Adopted scheme:** unique router per container + explicit
     `.service=` pointing at a config-hash service name: identical config
     merges (one service, all servers); a domain change on the newest
     router coexists (both hosts served, no conflict); a config change
     yields a second service and the newest (highest-priority) router
     wins — the single-container blue/green behavior.
2. **Healthcheck behavior.**
   - (a) A new server is **UP immediately** and receives its share of
     traffic until its first failed check (nginx failing `/missing`:
     served every other request for ~10s, then `DOWN` and ejected).
   - (b) **All servers down → 503**; one recovering → back to 200 within
     one interval, automatically.
   - (c) Identical healthcheck labels across containers merge cleanly;
     differing ones → see 1(d).

## As built (deviations from the design above)

- **Label scheme:** spike 1(e), not the shared router or the slot-1
  fallback (ADR 0018 §1).
- **Slot names:** slot 1 stays `dm-{slug}` (no rename of any existing
  container); extra slots `dm-{slug}-r{n}`; staged
  `dm-{slug}-r{n}-{deployID}` (slot 1 keeps `dm-{slug}-{deployID}`).
- **Ports:** OS-reserved per swap (`swap.ReserveLoopbackPort`, which
  every swap already used) and stored in `app_replicas.host_port`; no
  hash/linear-probe allocator. `preview_host_port` dual-written with
  slot 1's port.
- **Swap:** one `swap.Swap` per slot, looped — no separate N≥2 path, so
  the "unify the N=1 and N≥2 swap paths" debt does not exist.
- **Scale rows** record the *current* deployment as each new slot's
  `deploy_id` (no false drift) and leave `current_deployment_id` alone;
  they are excluded from build stats and cannot be rolled back to.
- **Drift badge** says "not current" rather than "older image": after a
  halted rollout the replaced slots run a *newer* deployment than the
  target.
- **Metrics/resource detection/restart alerts** still read slot 1 only
  (backlogged).
- **Logs:** the panel resolves the slot set when it connects — reload
  after scaling.
- **Verification:** unit tests per the list above; `make e2e-replicas`
  (throwaway server) covers scale 1→2→3→1 + `scale` rows, the label
  contract on both slots, `/preview` round-robin, stopped-slot failover
  (8/8 200s) + monitor heal, merged/filtered logs, a rolling redeploy at
  N=2 under preview load with **0 failed requests**, cap rejection, and
  delete removing every slot. Real-Traefik LB behavior is covered by the
  spikes above, not the e2e.
