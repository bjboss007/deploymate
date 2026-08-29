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
5. **Swap** — stop + remove `dm-<slug>`, create + start a new container
   with:
   - env = service connection URLs from the app's **environment** only
     (0003/0010/0017) + app env vars (decrypted) + `GIT_SHA`;
   - labels = ownership + Traefik routing (if domains exist);
   - network `deploymate-net`, `restart: unless-stopped`.
6. **Finish** — deployment `running`, app `running` +
   `current_deployment_id`, then prune images beyond the newest 5 per app.

**Rollback** skips steps 2–4: it reuses a kept `image_tag` and goes
straight to the swap. Rollback of a rollback is prevented in the UI.

**Failure** anywhere → deployment `failed` + `error`, app `failed`. The
previous container is untouched — a failed build never takes a running
app down.

## Manual (image) deploys

`POST /apps/{slug}/deploy` runs the same swap synchronously in the HTTP
handler (no worker): check `HasImage` → pull if missing → swap. This is
the P2 path; the git path is the worker path. Both build env through the
same `Server.AppEnv` function (single source of truth for env assembly).

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
