# 0018 — App replicas: slots, config-addressed Traefik services, rolling swaps

- **Date:** 2026-09-30
- **Status:** accepted

## Context

An app was exactly one container: no throughput scaling, no process-level
redundancy. The design (`docs/specs/app-replicas.md`, owner decisions
locked 2026-09-12) called for 1–5 identical replicas behind one address,
any-replica-up health, merged logs, and a ≥1-serving rollout floor, and
made two Traefik behaviors **blocking spikes**. The spikes ran on
2026-09-30 against Traefik v3.3 (the production pin) — on Docker Desktop
after all, by mounting the VM's `/var/run/docker.sock` rather than the
host socket path dev-environment.md warns about. Results are recorded in
the spec's "Spike results" addendum; the ones that shaped this ADR:

- Identical router + service labels on N containers merge into one
  round-robin service (the spec's assumption holds) — **but** when two
  containers define the same router **or** service name *differently*
  (a domain edited mid-rollout; a port or health-path change), Traefik
  drops that router/service entirely and **every request 404s**.
- A new server starts **healthy** and takes traffic until its first
  failed check; all-servers-down returns **503** and recovers on its own.

## Decision

1. **Label scheme (supersedes the spec's shared-router / slot-1-owns-router
   options):** every container owns a **unique router name**; its router
   points (`.service=`) at a **shared service named by a hash of the
   service config** — `{slug}-{crc32(port|health_path|interval|timeout)}`.
   Same config ⇒ same service ⇒ Traefik merges and round-robins; a
   config change ⇒ a new service name, never a conflicting redefinition.
   The newest deploy's routers carry the higher priority; when they all
   target one service the choice is immaterial. Active healthcheck labels
   (`health_path`, 10s interval, 3s timeout) are always emitted for routed
   apps. Verified live against real Traefik before adoption.
2. **Slot naming keeps slot 1 canonical:** slot 1 is `dm-{slug}` (every
   existing container and every single-replica app is unchanged — no
   rename, no migration of running containers); extra slots are
   `dm-{slug}-r{n}`. The spec's `dm-{slug}-r1` would have renamed every
   live app container.
3. **One swap, looped:** a rollout runs the existing `swap.Swap` once per
   slot, in slot order, so N = 1 is literally the pre-replicas code path
   and there is no second swap mechanism to drift (the spec's "named debt:
   unify N=1 and N≥2" never arises). A failed slot halts the rollout;
   replaced slots keep the new image, drift is shown per slot.
4. **Host ports come from the OS**, not the spec's hash + linear-probe
   allocator: every swap already reserves a free loopback port
   (`swap.ReserveLoopbackPort`), and the replica table stores what each
   swap recorded. `apps.preview_host_port` is dual-written with slot 1's
   port for one release.
5. **Scale is a no-build `scale` deployment** that starts missing slots
   from the current deployment's image (recording the *current*
   deployment as their `deploy_id`, so a scale never looks like drift)
   and removes the highest slots; it never touches slot 1 and never moves
   `current_deployment_id`. Scale slots' routers carry no priority so
   they never outrank the deploy's routers.
6. **The dashboard proxy is the LB where Traefik is absent** (macOS dev,
   and the live Cloudflare-tunnel server today): `/preview` and the
   public subdomain route rotate the start slot per request, put slots
   the monitor marked unhealthy last, and fail over to the next slot on a
   **dial** error only (the request never reached an app, so a retry
   cannot double-apply it; bodies replay only when re-readable).

## Consequences

- Traffic during a rollout on a *config change* (port or health path)
  moves to the new slots as soon as their routers appear — the same
  "newest wins at start" behavior the single-container blue/green swap
  always had under Traefik. With unchanged config, old and new slots
  share one service and traffic spreads across both.
- A broken new replica can receive ~1/N of Traefik traffic for up to one
  healthcheck interval (10s) before ejection (spike 2a). The dashboard LB
  has no such window: it only skips on the monitor's verdict or dial
  errors.
- The Traefik healthcheck requires 2xx/3xx on `health_path`, while the
  monitor probe accepts anything below 500. An app whose `/` returns 404
  is "healthy" to the monitor but ejected by Traefik — set a real health
  path (the UI control is a follow-up; the column exists).
- Metrics, resource detection, and restart alerts still sample slot 1
  only; limits are per container, so slot 1 is a fair proxy, but a sick
  non-canonical replica's resource usage is invisible (backlogged).
- A container predating this change carries the old label shape (service
  named after its router). Scaling it up before its first redeploy puts
  the new slot in a different Traefik service; the next deploy converges
  everything. The dashboard LB is unaffected.

## Alternatives

- **Shared router on every slot** (spec option A) — verified to merge,
  but any divergence (domains edited mid-rollout) drops the router: a
  full outage from an ordinary edit.
- **Slot 1 owns the router, others service-only** (spec fallback) — works,
  but each service-only container spawns an auto-router
  (`Host(<container-name>)`) and still shares one service name, so a
  port/health-path change during a rollout drops the service.
- **Stage without Traefik labels, add them after the probe** — labels are
  immutable; this needs a second container recreate per slot.
