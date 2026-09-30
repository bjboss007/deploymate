# Deploy flow (deep dive)

## State machine

```
           webhook push ─┐
 manual "Deploy" button ─┼─► queued ──(worker claims)──► building ──► running
 rollback button ────────┘        │                          │
                                  └──────────────────────────┴──► failed (error recorded)
```

Every transition is persisted. `deployments` rows carry `commit_sha`,
`commit_message`, `image_tag`, `error`, and timestamps; `build_logs` keeps
every build line with sequence numbers.

## Worker steps (`internal/jobs/worker.go`)

1. **Claim** — oldest `queued` row, atomic status guard (see database.md).
2. **Clone** — `git clone --filter=blob:none --depth 1 --branch <branch>`
   into `<data>/repos/<deployment_id>` with the source's deploy key; if a
   SHA was pinned by a webhook, fetch + checkout it. Checkout dir is
   removed after the deploy.
2b. **Manifest** — read `deploymate.yml` + `deploymate.{env}.yml` from the
   checkout and reconcile the app's environment (ADR 0017): reuse/start/
   provision the declared services (`Provisioner.Ensure`), so connection
   URLs are present in step 5's env assembly. Runs before the build so
   provisioning overlaps it; failures fail the deploy.
3. **Build** — one of two engines, chosen by `apps.runtime`:
   - `""` (Dockerfile): `docker buildx build --progress=plain --load
     -t deploymate/apps/<slug>:<deployment_id>`
   - `key[:version]` (runtime): `railpack build` with
     `BUILDKIT_HOST=docker-container://dm-buildkit` (a `moby/buildkit`
     container); a pinned version is written as `.mise.toml` unless the
     repo has its own; the image lands in the local daemon via
     railpack's built-in `docker load` pipe.
   Every line → `build_logs` + SSE topic `deploy:<slug>` (the deployment
   page replays history on connect, so a reload loses nothing).
4. **Record** — deployment gets `image_tag`; an `images` row is created.
5. **Swap** — per replica slot, in order (1 = `dm-<slug>`, then
   `dm-<slug>-r2…r5`): stage a new container beside the slot's old one,
   probe it, record its port in `app_replicas`, remove the old one, and
   rename the staged one into the slot. A failed slot halts the rollout
   (earlier slots keep the new image — drift shows per slot). Slots above
   `apps.replicas` are removed at the end. Each new container gets:
   - env = service connection URLs from the app's **environment** only
     (0003/0010/0017) + app env vars (decrypted) + `GIT_SHA`;
   - labels = ownership + Traefik routing (if domains exist);
   - network `deploymate-net`, `restart: unless-stopped`.
6. **Finish** — deployment `running`, app `running` +
   `current_deployment_id`, then prune images beyond the newest 5 per app.

**Rollback** skips steps 2–4: it reuses a kept `image_tag` and goes
straight to the swap. Rollback of a rollback is prevented in the UI.

**Manual deploys** (`kind=manual`, the dashboard Deploy button) also skip
steps 2–4: no repo, so no clone/build/manifest — the persisted image is
pulled if missing (bounded at 10 min so a hung registry can't hold the
serial worker) and the swap runs.

**Failure** anywhere → deployment `failed` + `error`, app `failed`. A
failed build's `error` names the cause: out-of-memory kills are reported
as such (`builder.ErrOutOfMemory`, with what to do), anything else
quotes the builder's last error line — never a bare "exit status 1". The
previous container is untouched — a failed build never takes a running
app down.

## Replicas (1–5 per app)

ADR 0018, spec `docs/specs/app-replicas.md`. `apps.replicas` is the
desired slot count; `app_replicas` records each running slot (container,
loopback port, last probe verdict, deployment it runs). An app with no
rows (not redeployed since replicas shipped) resolves to one synthetic
slot 1 — `appspec.Slots` is the single resolver every reader uses.

- **Scale** (`POST /apps/{slug}/replicas`, 1–5): a running deployed app
  queues a no-build `kind=scale` deployment — missing slots start from the
  current deployment's image, extra slots are stopped and removed; slot 1
  and `current_deployment_id` are never touched. Otherwise the count is
  just recorded and the next deploy converges.
- **Deploy/rollback/resize** roll every slot, one at a time (≥ N-1 serve
  throughout; N = 1 is the old swap exactly).
- **Routing:** the dashboard `/preview` and public-subdomain proxy
  round-robins over the slots, skips ones the monitor marked unhealthy
  (unless all are), and fails over on dial errors. Under Traefik each
  container owns a router pointing at a shared config-hash service with an
  active healthcheck on `apps.health_path`.
- **Health:** the monitor probes every slot at `health_path`; the app is
  healthy when any slot answers (the page shows "2/3 replicas up").
  `health_path` is set in the Replicas panel (`POST
  /apps/{slug}/health-path`): saving probes every running replica and
  warns about any that don't return 2xx/3xx — Traefik's healthcheck
  (which gets the new path on the next deploy) would eject them.
  Per-slot heal (start, rebind, or recreate a vanished extra slot from
  slot 1's image) never runs while a deployment is queued/building.
- **Lifecycle:** start/stop/restart/delete fan out to every slot. Swaps
  and scale-downs drain: the old container gets a 10s SIGTERM grace
  (`docker stop`) before removal; scale-down drops the replica rows first
  so the dashboard LB stops routing to the slot before it stops.
- **Logs:** one SSE merging every slot with `[rN]` prefixes;
  `?replica=r2` (the panel's filter) narrows to one slot. The stream
  re-resolves replicas every 5s, so scaling shows up without a reload.
- **Metrics:** sampled per replica (`metrics.slot`, one timestamp per
  tick); charts show the total or one replica (`/metrics?replica=r2`);
  limits are sized from the busiest replica; pressure on any replica
  triggers the auto-resize; restart alerts are per replica.
- **Scale on a pre-replicas app:** a routed slot 1 without the
  `deploymate.slot` label is relabelled (zero-downtime swap) before new
  slots start, so every slot shares one Traefik service.

## Manual (image) deploys

`POST /apps/{slug}/deploy` persists the form (image/port) and **queues** a
`manual` deployment row — the request never blocks on a pull. The worker
(`runManualDeploy`) owns everything after that: check `HasImage` → pull if
missing (**bounded: 10 min** — a hung registry fails the deploy instead of
holding the worker forever) → the shared zero-downtime swap → `finish`.
Manual deploys skip the clone/build/manifest steps (no repo); they wait
their turn behind queued git builds — deploys serialize by construction.
Progress and failures land on `/deployments/{id}` (build-log SSE), exactly
like git deploys; a failed pull never flashes on the app page anymore, and
a failed swap leaves the old container serving. Both paths build env
through the same `Server.AppEnv` function (single source of truth).

The form also carries optional **entrypoint/command overrides** (two
whitespace-separated fields, no quoting; empty = the image's own values).
They persist on the app row (`apps.entrypoint`/`apps.command`) and flow
into every rebuild of an image app — deploys, rollbacks, restarts, and
binding heals read them through `appspec.SplitArgs` at spec time. Git
apps always have empty columns and are unaffected.

## Webhook → deploy (timing)

1. Provider POSTs `/hooks/{id}`; handler verifies HMAC, dedupes the
   delivery, filters branches, inserts `queued`, replies 200 in <50 ms.
2. Worker polls every 1 s → claims → builds (seconds to minutes).
3. Dashboard: `/deployments/{id}` page live-updates via SSE
   (`/deployments/{id}/stream` replays stored lines, then streams
   new events).

## Where things live on disk

| Path | Contents |
|---|---|
| `<data>/data.db` (+ `-wal`, `-shm`) | all metadata |
| `<data>/keys/root.key` | master encryption key (0600) |
| `<data>/repos/<deployment_id>/` | transient checkouts (removed after deploy) |
| `<data>/letsencrypt/` | Traefik ACME storage (acme.json) |
| named volumes `dm-svc-<slug>-data` | database/cache data |
| docker images `deploymate/apps/<slug>:<id>` | rollback artifacts |
