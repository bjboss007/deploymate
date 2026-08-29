# 0015 — Automatic resource detection & port mapping

- **Date:** 2026-08-29
- **Status:** accepted

## Context

Setting CPU/memory limits by hand is exactly the work a PaaS should
remove — and the platform already samples every app's real usage every
5 s. Separately, Railpack's runtime contract is `$PORT`: its start
commands (`java -Dserver.port=$PORT …`, node/python servers) read the
port from the environment, which DeployMate never injected (VGG only
worked because Spring Boot silently defaulted to 8080).

## Decision

1. **Resource limits are derived, not configured.** Hourly, the monitor
   computes each app's P90 memory and P90 CPU over the trailing 24 h of
   its own metrics, doubles for headroom, clamps (64 MB–4 GB memory,
   0.5–4 CPU), and stores the result on the app. Limits apply as docker
   `--memory`/`--cpus` at the **next deploy** (docker requires a
   recreate to change them). Apps without 10+ samples run unlimited
   until they have history. MemorySwap = 2× the limit so GC-heavy apps
   (Spring Boot) swap rather than OOM-die at the limit edge.
2. **Port mapping is a platform convention:** every app container gets
   `PORT=<routing port>` injected; new apps default to **8080**
   (Spring Boot/Heroku convention), overridable in the UI. Railpack
   images need no config to honor it; the preview/health loops keep
   using the same port.
3. Detection and application are separate loops: detection is
   continuous, application is per-deploy — limits never change under a
   running app, and the UI states plainly when the applied limit is
   pending the next deploy.
4. **Auto-resize trigger (the self-healing half):** when sustained usage
   passes 80% of the *applied* limit (10-minute P90 window), the monitor
   bumps the limit and queues a `resize` deployment — a no-build swap of
   the current image, seconds of downtime — before the OOM happens.
   Cooldown (30 min per app) is enforced via the deployments table so it
   survives restarts. `resource_resized` alert fires per resize. Verified
   live: squeezing VGG to 256MB triggered the resize; the rebuilt
   container came up with the re-derived limit.

## Consequences

- Verified on live apps: detector assigned 64 MB/0.5 CPU to the tiny
  node/python/nginx apps and **621 MB** to Spring Boot's VGG, from
  thousands of real samples. Limits and `PORT` confirmed in
  `docker inspect` after redeploy.
- A memory-hungry app that outgrows its limit gets a bump on the next
  deploy automatically — no alert needed at this stage; the health
  probe surfaces OOM crashes meanwhile.
- Manual limits remain possible (the fields exist), but the UI has no
  controls for them — that's the point. If a specific app needs an
  override, that's a future per-app pin, not a global default.

## Alternatives

- **UI-only limit controls** (the original backlog idea) — manual
  configuration is a guessing game; the metrics were already there.
- **Live limit updates** — would require container recreation on a
  schedule; per-deploy application is the same guarantee with zero
  downtime risk.
