# Prebuilt deploys (CI builds, DeployMate runs) — specification

**Status:** **P0 and P1 implemented 2026-10-02** (ADR 0019): per-source
delivery key + event dispatch, migration 0017, the GitHub client, the six
`workflow_run` gates, `runArtifactDeploy`, and `make e2e-artifact`. **P2 implemented
2026-10-02** (UI: mode select, write-only token, Test connection with the
private-repo scope warning, generated workflow, "Deploy latest successful
run"); **P3** (memory preflight) is not built. The real project passed an
end-to-end test the same day (see progress.md). The `seed-git-source` hook
(`DEPLOYMATE_SEED_MODE` / `_API_TOKEN`) remains for tests. Owner constraints:
no builder machine, no container registry. Trigger facts and GitHub limits
below were checked against GitHub's docs on 2026-10-02 (see "Verified
facts"); three items are marked **spike** and must be proven on real GitHub
before the feature code is written. Backlog: "Off-box builds for small
servers" in `docs/improvements.md`. Related: `docs/specs/app-replicas.md`,
`internal/webhooks`, `internal/jobs/worker.go`, `internal/builder`.

## Problem

DeployMate builds every git app **on the server that serves traffic**. A
JVM build (Gradle daemon with a 2 GiB heap + compilers + BuildKit) peaks at
~2.5–3 GB, while the same app *runs* in ~0.3–0.6 GB. Found live
(2026-09-30): the owner's `testing` app (`BeyondCredit/trade-stack-backend`,
Java 21 / Gradle) died with `ResourceExhausted … cannot allocate memory` on
a 3.8 GiB Docker Desktop VM already holding ~1.3 GiB of containers. On the
2–4 GB servers this project targets it can never build there. It is also
slow: the failed build spent ~3 min installing Java + Gradle and ~2 min
downloading the Gradle wrapper before compiling a file (no build cache).

**Goal:** let CI (which has the RAM) do the compile, and have DeployMate
only *run* the result — through the same zero-downtime, replica-aware,
rollback-able pipeline, with no new infrastructure to own.

## Options considered

| | **A. Prebuilt artifact** (recommended v1) | **B. Registry image** | C. Leaner on-box builds |
|---|---|---|---|
| CI produces | the JAR (`upload-artifact`) | a runtime image pushed to GHCR | nothing — builds on the server |
| DeployMate fetches | artifact zip via GitHub API | image pull | clone + build |
| Wraps/builds on server | 2-line Dockerfile (`FROM jre` + `COPY`): seconds, ~100 MB RAM | nothing | full build, ~1–1.5 GB with tuning |
| Credential on the server | **fine-grained** token, **one repo**, `Actions: read` | **classic** PAT, `read:packages` — account-wide, cannot be limited to one repo | none |
| External dependency | GitHub Actions only | Actions + ghcr.io | none |
| Storage/quotas | artifacts count toward the **shared Actions/Packages quota** (Free 500 MB, Pro/Team 2 GB); mitigated with `retention-days: 1` | GHCR storage + bandwidth "currently free" per GitHub's docs (policy can change) | none |
| Architecture | JAR is arch-neutral; base `eclipse-temurin` is multi-arch | CI must build the image for the server's arch (amd64 server vs arm64 Mac) | native |
| Promote one build across dev→stage→prod | each env re-wraps the same commit's JAR (same bytes, different image) | identical image by digest | n/a |
| Works for 2 GB | **yes** (run only) | **yes** | **no** for JVM builds |
| Trigger | `workflow_run` on the existing webhook | `workflow_run` (recommended) or `registry_package` (payload paths undocumented; GitHub recommends the newer `package` event) | git `push` |

**Decision: build A first.** It needs no registry (owner constraint), uses
the narrowest credential, and its failure surface is the smallest. B stays a
documented v2 (it shares the trigger, the mode column and the worker seam —
see "Registry route (v2)"). C's *preflight* ships with A (below); its
tuning knobs stay in the backlog.

## Design (route A)

### Flow

```
git push ─► GitHub Actions: ./gradlew bootJar ─► upload-artifact (app.jar, 1 day)
                    │
                    └─► workflow_run "completed" ─► DeployMate  POST /hooks/{id}   (existing webhook)
                                                         │ verify HMAC, gate (below), queue deployment
              worker: GET run's artifacts ─► GET artifact zip (302 → short-lived blob URL)
                    ─► read the ONE jar out of the zip ─► FROM eclipse-temurin:{ver}-jre + COPY
                    ─► docker build (seconds) ─► tag deploymate/apps/{slug}:{deployID}
                    ─► the existing rolling, replica-aware swap  (rollback/prune/stats unchanged)
```

### Model (migration 0017)

- `apps.deploy_mode` — `build` (default; today's behavior, untouched) |
  `artifact`. Offered only for `provider = github` sources.
- `apps.workflow_path` — default `.github/workflows/deploymate.yml`; only
  runs of **this** workflow deploy this app (so a PR-check or lint workflow
  in the same repo can never trigger a deploy).
- `apps.artifact_name` — default `deploymate-app`.
- `git_sources.api_token_enc` — fine-grained PAT, encrypted with the same
  AEAD envelope as deploy keys (ADR 0008). **New column, not `pat_enc`:**
  `pat_enc`/`clone_method='pat'` exist but are unused, and an API token is a
  different credential from a clone credential — conflating them would make
  a future HTTPS-clone feature ambiguous.
- `deployments.ci_run` (GitHub run id) and `deployments.ci_run_number`
  (monotonic per workflow) — idempotency and ordering.
- `deployments.trigger` gains `ci`. Kind stays `deploy`, so stats, history,
  releases, rollback and image pruning need no changes. The worker branches
  inside `runGitDeploy` on `app.DeployMode`.
- The existing `apps.runtime` (e.g. `java:21`) selects the JRE base image
  tag for the wrapper — no second "which Java" setting.

### Webhook: dispatch by event (prerequisite P0 + P1)

`handleWebhook` today assumes every delivery is a push (it parses `ref` and
compares to the branch; `workflow_run` has no `ref`, so it is currently
harmlessly answered "ignored: not the deploy branch"). It will dispatch on
`X-GitHub-Event`:

- `ping` → `pong` (confirms the hook + secret when the owner adds it).
- `push` → today's path, **only for apps in `build` mode**; for `artifact`
  apps a push is ignored (CI will report back when its build is done).
- `workflow_run` → new path, **only for `artifact` apps**, for each app
  linked to the source, all of these must hold (payload is HMAC-verified
  first, as today):
  1. `action == "completed"` and `workflow_run.conclusion == "success"`;
  2. `workflow_run.path == app.workflow_path`;
  3. `workflow_run.head_branch == source.default_branch`;
  4. `workflow_run.event` is `push` or `workflow_dispatch` — **never
     `pull_request`**: a fork PR whose branch is literally named like the
     deploy branch would otherwise get its (attacker-built) artifact
     deployed;
  5. `workflow_run.head_repository.full_name == repository.full_name`
     (same-repo runs only — second guard for 4);
  6. ordering/idempotency: skip if a `ci` deployment with the same
     `ci_run` exists, or if `run_number` is **lower than or equal to** the
     latest non-failed `ci` deployment's (an older run finishing late, or a
     "Re-run" of an old workflow, must not roll production back).
  Skipped runs answer `200` with the reason, like today's "ignored" paths.

**P0 (hard prerequisite, same change):** the delivery de-dupe key becomes
`provider:sourceID:deliveryID`. GitHub sends every hook on a repo the
**same `X-GitHub-Delivery` GUID** for one event (documented bug in
improvements.md, found live on the three-environment demo); without the fix
only the first of dev/stage/prod would ever deploy from one `workflow_run`.
Add the two-apps-one-repo assertion to `make e2e-git`.

### Worker: `runArtifactDeploy`

1. **Find the artifact:** `GET /repos/{o}/{r}/actions/runs/{run_id}/artifacts`
   → entry with `name == app.artifact_name`, `expired == false`,
   `size_in_bytes ≤ cap` (default 400 MB, constant). Each failure has its
   own message (see "Failure messages").
2. **Download:** `GET …/artifacts/{id}/zip` → **302** to a blob URL that
   expires in **1 minute** → follow it **without** the `Authorization`
   header (Go's client drops it on a cross-host redirect; asserted in a
   test) → stream to a temp file under `data/builds/{deployID}/` with a hard
   byte cap. If the API gave a `digest`, verify it; always record the
   sha256 in the build log.
3. **Extract exactly one JAR, never by entry name:** iterate the zip,
   accept entries matching `*.jar` excluding `*-plain.jar`, require exactly
   one (zero or many → fail listing the names), copy that entry's stream
   to a fixed filename `app.jar` with a size cap. Nothing is written using
   an attacker-controlled path, so zip-slip, symlinks and nested-archive
   tricks have nowhere to land; zip bombs are bounded by the copy cap and an
   entry-count cap.
4. **Wrap:** render a Dockerfile from a template (Java v1):
   ```dockerfile
   FROM eclipse-temurin:21-jre
   ENV JAVA_OPTS="-XX:MaxRAMPercentage=75"
   RUN useradd -r -u 10001 app
   COPY --chown=app app.jar /app/app.jar
   USER app
   ENTRYPOINT ["sh", "-c", "exec java $JAVA_OPTS -Dserver.port=${PORT} -jar /app/app.jar"]
   ```
   (`$JAVA_OPTS` and `PORT` follow the Railpack conventions the platform
   already uses; a user-set `JAVA_OPTS` env var overrides the image default;
   the app's Entrypoint/Command override fields still win.) Build with the
   existing `builder.Build` — failures get the new out-of-memory/last-error
   diagnosis for free.
5. **Record + swap:** `CreateImage` (so rollback, prune and the /stats disk
   panel work), then the existing `runContainer` rolling swap. `commit_sha`
   and message come from `workflow_run.head_sha` / `head_commit.message`, so
   the releases page and the diff-review page keep working. `GIT_SHA` is
   injected as today.
6. **Rollback** reuses the kept local image tags — no GitHub call, so
   rollback keeps working after the artifact expires or the token dies.

### Credential handling

- Token scope: **fine-grained PAT, single repository, `Actions: read`**
  (verified: this is the exact permission the three artifact endpoints
  need). Nothing else. Stored encrypted; never logged; the UI shows only
  "set" / "replace" (write-only field).
- **Save-time check:** "Test connection" calls `GET /repos/{o}/{r}` and the
  workflow's runs list and reports exactly what failed (404 on a private repo
  = no access to *that repo*; 401 = bad/expired token; 403 = missing
  permission or org SSO/approval pending).
- **Scope warning (from spike S1-B):** DeployMate cannot enforce "one
  repo", so Test connection also asks `GET /user/repos?per_page=100&
  affiliation=owner,organization_member` and, when the token can see
  **private repositories other than this one**, shows an amber warning:
  "this token can also read Actions artifacts of N other private
  repositories — create one limited to *Only select repositories →
  {repo}*". **Only private repos count**: a fine-grained token can always
  read public repos (verified — a correctly narrowed token still listed 91
  public repos and returned 404 for the private ones), so counting them
  would warn every user. (`githubci.Client.OtherPrivateRepos` implements
  this and is tested; the UI that shows it is P2.) It **warns, never
  blocks** (some owners legitimately share one token across a
  monorepo-adjacent set). The field help text and the docs say how to
  create the token (Only select repositories, Actions: read, short expiry).
- Fine-grained PATs **expire** (and an org may force short lifetimes or
  approval). A deploy that gets 401/403 fails with "the GitHub token was
  rejected — replace it on the app page" and fires the existing
  deploy-failed alert. (Surfacing the expiry date up front is a follow-up;
  needs a spike on the response header.)

### UI (app page, Git panel, GitHub sources only)

- **Deploy mode** select: *Build on this server* | *Prebuilt (GitHub
  Actions)*.
- In prebuilt mode: token field + **Test connection**; workflow path and
  artifact name (defaults shown, editable); a **generated workflow file**
  with a copy button, filled with the tracked branch, the app's Java
  version and the artifact name; the one-time instruction "in the repo's
  webhook settings, tick **Workflow runs**"; and **Deploy latest successful
  run** — lists the newest successful runs of the workflow on the tracked
  branch (`GET …/actions/workflows/{path}/runs?branch=…&status=success`) and
  queues the latest not-yet-deployed one. That button is also the answer to
  a missed webhook (GitHub does not auto-retry failed deliveries) and to the
  first deploy (no event exists yet).
- While prebuilt, the "Review & deploy…" diff flow stays (it needs only the
  mirror clone, unchanged) but its deploy button reads "Deploy latest
  successful run".

Generated workflow (Gradle example; Maven is the same shape):

```yaml
name: DeployMate build
on:
  push:
    branches: [development]        # = the app's tracked branch
permissions:
  contents: read
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-java@v4
        with: { distribution: temurin, java-version: "21", cache: gradle }
      - run: ./gradlew bootJar -Pproduction --no-daemon
      - run: mkdir out && cp "$(ls build/libs/*.jar | grep -v -- -plain | head -n1)" out/app.jar
      - run: cp deploymate.yml deploymate.*.yml out/ 2>/dev/null || true
      - uses: actions/upload-artifact@v4
        with:
          name: deploymate-app
          path: out/
          retention-days: 1          # keeps shared Actions/Packages storage near zero
          if-no-files-found: error
```
The `cp … out/app.jar` step is what makes multi-module Gradle builds safe:
the artifact always holds exactly one JAR, so DeployMate never has to
guess which JAR is the application.

**Infra manifests (added 2026-10-04).** The next step copies the repo's
`deploymate.yml` / `deploymate.{env}.yml` into `out/`, so they travel in the
artifact — no extra token permission (fetching them through the contents API
would need *Contents: read*, which prebuilt mode deliberately does not ask
for). The worker (`builder.ExtractManifests`, `Worker.applyManifest`) takes
only exact root-level names, ≤64 KiB each, and provisions the declared
services exactly like a git build (same `LoadManifest` overlay rules, same
`resolveManifest` logging). A malformed manifest fails the deploy before the
build. **Workflows copied before this change keep working but upload no
manifest**: re-copy the workflow from the app page to opt in.

### Memory preflight (ships with A)

Independent of mode, before an **on-server** build of a JVM runtime
(`java:*`) DeployMate compares the host's Docker memory budget against a
conservative requirement (~3 GB) and, when short, writes one clear line to
the build log and an amber note on the app page: "this build needs ≈3 GB
free; this host has X — switch the app to Prebuilt (GitHub Actions)". It
**advises, never blocks** (the estimate is rough; the builder's own OOM
diagnosis remains the backstop). Source: `docker info` MemTotal minus the
sum of running containers' usage — the same data the /stats page already
reads.

## Failure messages (each actionable)

| Situation | Message (summarized) |
|---|---|
| token 401 / 403 / 404 | "GitHub token rejected for owner/repo (HTTP …) — create a fine-grained token with Actions: read on this repo and replace it" |
| run has no artifact of that name | "workflow run N succeeded but uploaded no artifact named X — check `upload-artifact` name" |
| artifact `expired` | "artifact expired — re-run the workflow or use Deploy latest successful run" |
| artifact over the size cap | "artifact is N MB, over the M MB limit" |
| 0 or >1 `.jar` in the zip | "expected exactly one .jar in the artifact, found: a.jar, b.jar" |
| digest mismatch | "artifact failed its integrity check" |
| wrapper build fails | the new builder diagnosis (OOM / last error line) |

## Security

- Same trust boundary as today: whoever can push to the tracked branch (and
  edit the workflow file) already controls what runs; build mode runs their
  Dockerfile on the server. Nothing here widens that, and the event gate
  (no PR/fork runs, workflow path match) closes the new surface the
  artifact route would otherwise add.
- The webhook secret is verified before any payload is read; the artifact
  API call is the second proof the run exists and succeeded.
- Redirect URLs carry a signed token: never logged, `Authorization` never
  forwarded to the blob host.
- **Blast radius of the stored token** = whatever repos it was created for
  (read of Actions runs/artifacts only; it can neither read code nor start
  workflows — verified). An all-repositories token exposes every repo's
  build artifacts if DeployMate's database **and** key file leak together;
  hence the Test-connection scope warning and the "Only select
  repositories" guidance.
- Artifacts are readable by anyone with repo read access: keep secrets out
  of the JAR (config comes from DeployMate env vars, 12-factor as already
  documented).
- Wrapper base image is the official `eclipse-temurin` JRE (floating
  `21-jre` tag picks up security patches on each wrap; digest pinning is a
  follow-up). Docker Hub anonymous pull limits apply to the first pull per
  host only (layers are then cached) — spike S2 measures it.

## Verified facts (GitHub docs, 2026-10-02)

- Download artifact: `GET /repos/{o}/{r}/actions/artifacts/{id}/zip` →
  **302**, redirect URL **expires after 1 minute**; list per run:
  `GET /repos/{o}/{r}/actions/runs/{run_id}/artifacts`; response fields
  `expired`, `expires_at`, `size_in_bytes`, `digest`.
- Fine-grained PAT permission for all three artifact endpoints:
  **"Actions" — read.** (Classic tokens need `repo` for private repos.)
- `workflow_run` actions `requested | in_progress | completed`; fields
  `workflow_run.{id, head_sha, head_branch, conclusion, status, event, name,
  path, head_commit.message}`, `repository.full_name`; delivered when the
  hook has the **"Workflow runs"** event enabled.
- Packages quota (Free 500 MB / Pro 2 GB / Team 2 GB / Enterprise Cloud
  50 GB) is **shared with Actions artifacts**; ghcr.io storage and bandwidth
  are "currently free". `retention-days` can be set per upload (bounded by
  the repo/org/enterprise retention limit).
- GHCR private pulls from a server require a **classic** PAT
  (`read:packages`); fine-grained PATs and GitHub Apps are not documented as
  supported (relevant to route B only).

## Spikes (before feature code)

- **S1 — real GitHub round trip.** **Part A done 2026-10-02** on a
  throwaway private repo (`bjboss007/dm-artifact-spike`: the generated
  workflow shape, a `javac`-built 1.6 KB JAR, `retention-days: 1`), with a
  scratch DeployMate behind a temporary Cloudflare quick tunnel so GitHub's
  deliveries hit the real P0 handler. Confirmed against real GitHub:
  - **Delivery + P0:** `ping` → `pong`; a run produces three `workflow_run`
    deliveries (`requested`, `in_progress`, `completed`, distinct GUIDs), all
    answered `200 "ignored: not a push event"` by the P0 handler. Real HMAC
    verification works; headers are `X-GitHub-Event: workflow_run`.
  - **Payload fields (all present, as designed):** `action`;
    `workflow_run.{id, name, path, event, status, conclusion, head_branch,
    head_sha, run_number, run_attempt, workflow_id, display_title,
    pull_requests (empty for push), head_commit.message,
    head_repository.{full_name, fork}}`; `repository.full_name`. For a push
    run: `event == "push"`, `path == ".github/workflows/deploymate.yml"`,
    `head_repository.full_name == repository.full_name`, `fork == false`.
    (`run_attempt` exists: gate 6's idempotency can key on id + attempt.)
  - **Artifacts API:** `GET …/runs/{id}/artifacts` returns `id, name,
    size_in_bytes, expired, created_at, expires_at, archive_download_url,
    workflow_run{id, head_branch, head_sha}` and **`digest`
    (`"sha256:<hex>"`, populated)** — it is the digest of the **zip
    archive** and matched `shasum -a 256` of the downloaded file exactly.
  - **Download:** `GET …/artifacts/{id}/zip` → **302** to
    `productionresultssa7.blob.core.windows.net` (Azure blob; the host may
    change — follow whatever Location says); following it **without** an
    `Authorization` header → 200 + the zip. The zip held exactly `app.jar`.
  - **Retention:** `expires_at` = `created_at` + 1 day for
    `retention-days: 1`.
  - **Failure shapes:** no token → **401** "Requires authentication"; bad
    token → **401** "Bad credentials"; unknown artifact/run → **404** "Not
    Found"; rate limit **5000/hour** per token.
  **Part B done 2026-10-02** with a real **fine-grained token (`github_pat_…`,
  Actions: read)** the owner created:
  - **Works:** `GET /repos/{r}` (what Test connection needs; response header
    `x-accepted-github-permissions: metadata=read`),
    `GET …/actions/workflows/{file}/runs?branch=&status=success&event=push`
    (the "Deploy latest successful run" list: `workflow_runs[].{id,
    run_number, event, conclusion, head_sha, run_attempt}` all present),
    the run's artifacts, and the 302 download (follow without
    `Authorization`; zip sha256 == `digest`). Every call DeployMate needs.
  - **Permissions are least-privilege:** contents → **403**, dispatching the
    workflow (Actions: write) → **403**, both "Resource not accessible by
    personal access token".
  - **Repository scope is NOT enforced by DeployMate and was too wide in
    practice:** this token was created with *All repositories*, so it could
    read Actions **runs and artifacts of every private repo the owner has**
    (98 repos visible, 7 private; contents still 403). The spec's "one repo"
    is a property of how the owner creates the token, not something we can
    guarantee → see "Credential handling" (Test connection now warns).
  - **Token-expiry header: not returned** (`github-authentication-token-
    expiration` was absent for this token) — do **not** build an
    "expires soon" warning on it; surface expiry only as the 401 failure
    message. Also: the repo JSON's `permissions` object describes the
    *owner's* rights, not the token's (it said admin) — never use it to
    judge the token.
  **Still open (owner action):** repeat under the **`BeyondCredit` org**
  (org policy may require approval/SSO for fine-grained tokens).
- **S2 — wrapper cost on the target box.** ~~Run on the dev Mac~~
  **measured 2026-10-02 (Docker Desktop, arm64, 3.8 GiB VM)** — re-run on
  the real 4 GB Ubuntu box when it is up (numbers below are dominated by
  I/O, so expect the same shape):
  - cold pull of `eclipse-temurin:21-jre`: **42 s, 348 MB image** (once per
    host; layers are then cached and shared by every Java app);
  - the wrapper build (the exact `docker buildx build --progress=plain
    --load` the builder runs; 1 `COPY` layer + `useradd`): **1.4 s**; the
    engine's resident memory peaked at **~184 MB — no measurable increase**
    over its ~200 MB idle (sampled every 100 ms from inside the VM);
  - the wrapped image is the JRE image **plus the JAR** (+6 KB for the
    fixture; a Spring Boot fat JAR adds its own ~50–100 MB per deploy
    layer — the existing prune keeps 5 images/app);
  - the template behaves: runs as non-root `app`, honors `PORT` via
    `-Dserver.port`, ships `JAVA_OPTS=-XX:MaxRAMPercentage=75`, and the
    fixture server serves under a **128 MB** container limit using
    **28 MiB** idle;
  - Docker Hub anonymous pulls: headers show **100 pulls / 3600 s per IP**
    (`ratelimit-limit: 100;w=3600`); a wrap needs the base only on the
    first deploy per host, so the limit is irrelevant at this volume.
  Caveat: the fixture is a hello-world, not a Spring Boot app — real JVM
  runtime memory (~300–600 MB) is the app's, not the wrapper's.
- **S3 — retention accounting.** **Partly done:** `expires_at` honors
  `retention-days: 1` (above). *Not observable from here:* how fast expired
  artifacts stop counting toward the shared Actions/Packages quota, and
  current usage — the billing endpoints return 404 without the `user`
  scope. Owner check: Settings → Billing → Actions storage on
  `bjboss007` a day after the spike run (artifact expires 2026-10-03
  11:32 UTC) should show ≈0. Nothing in the design waits on this: a 1-day
  retention bounds usage by (pushes/day × JAR size) and route B remains the
  escape hatch.

## Verification

Unit: payload parsing; the six `workflow_run` gates (fork PR, wrong path,
wrong branch, failed conclusion, `push` ignored for artifact apps,
`workflow_run` ignored for build apps); idempotency + out-of-order
`run_number`; per-source delivery key (two sources, same GUID → both
deploy); artifact selection (expired, wrong name, over cap); zip extraction
(zip-slip names, symlink entries, `-plain.jar` exclusion, multiple/zero
jars, bomb caps); Dockerfile template; GitHub client against an
`httptest` fake (302 to a blob host that asserts **no** `Authorization`,
401/403/404 messages, digest verify); preflight arithmetic.

E2e (`make e2e-artifact`, throwaway server, no real GitHub): a tiny fake
GitHub API server + the committed 1.6 KB fixture JAR
(`testdata/apps/hellojar/hello.jar`, source + `build.sh` alongside — a
`com.sun.net.httpserver` hello-world answering "deploymate e2e prebuilt jar
fixture") → a signed `workflow_run` webhook → worker downloads from the
fake, wraps, runs → `/preview` serves the fixture's body; plus rollback to
the previous run, a second source with the same delivery GUID, and the
failure messages. The fake is addressed through a **test-only config var**
(`DEPLOYMATE_GITHUB_API_URL`, default `https://api.github.com`).
Real-GitHub behavior is covered by spike S1, not the e2e.

## Phasing

1. **P0 — DONE 2026-10-02** — per-source delivery key + dispatch on
   `X-GitHub-Event` (`ping` → pong, `push`/missing → deploy, everything
   else ignored — `workflow_run` is acknowledged and ignored until P1 adds
   its path) + the e2e assertion (verified failing on the old code). It
   shipped alone and fixed the live fan-out bug.
2. **P1 — DONE 2026-10-02** — spikes S1/S2 (S3 partial), migration 0017,
   GitHub client (`internal/githubci`), the six gates (`handlers_ci.go`),
   `runArtifactDeploy` (`internal/jobs/artifact.go`), safe unzip + wrapper
   template (`internal/builder/artifact.go`), failure messages,
   `make e2e-artifact` (fake GitHub: `testdata/fakegithub.py`, fixture
   `testdata/apps/hellojar/`).
3. **P2** — UI (mode, token + Test connection, workflow generator, Deploy
   latest successful run).
4. **P3** — memory preflight advisory.

## Registry route (v2, only if A proves insufficient)

Same trigger (`workflow_run`, not `registry_package`), same mode column
(`registry`), same worker seam; CI builds + pushes
`ghcr.io/{owner}/{repo}:{sha}` with `GITHUB_TOKEN`; DeployMate pulls with a
stored credential, **pins the digest**, verifies the image's
`org.opencontainers.image.revision` label equals `head_sha`, records the
pulled reference in `images` (today only built tags are recorded, so
pruning and rollback would otherwise ignore it), and rolls out. Needs: a
registry-credential store with `PullOptions.RegistryAuth` (today
`PullImage` sends none), a **classic** `read:packages` PAT (account-wide —
prefer a dedicated machine user with read-only access to that one package),
and CI that builds for the server's architecture. Choose it when you want
build-once/promote-by-digest across environments or multi-server pulls.

## Review pass (self-critique, folded in)

- **Quota is the artifact route's real weakness** → `retention-days: 1`,
  spike S3, and route B as the documented escape hatch.
- **Missed delivery + 1-day retention could lose the artifact** → "Deploy
  latest successful run" works for any non-expired run; a periodic
  reconcile poll is a backlog item, deliberately not v1 (more API traffic,
  more state).
- **Out-of-order and re-run deploys** → `run_number` ordering + `ci_run`
  idempotency (gate 6).
- **Fork-PR artifact poisoning** → gates 4 and 5.
- **Three environments, one repo** → P0 key fix; each env app has its own
  tracked branch/source, so each `workflow_run` deploys only its branch.
- **Multi-module / fat-JAR ambiguity** → the generated workflow publishes
  exactly one file; DeployMate refuses anything else rather than guessing.
- **JRE too old for the JAR** (compiled with a newer Java) → surfaces as
  `UnsupportedClassVersionError` in the app log; the generated workflow
  pins the same Java version as the app's runtime setting.
- **Shared-trigger confusion** (an app switched build↔prebuilt mid-flight)
  → mode is read per delivery; events for the other mode are ignored, so a
  switch never double-deploys.

## Out of scope (v1)

- GitLab/Gitea CI triggers (`workflow_run` is GitHub's) — sources on other
  providers stay build-mode.
- Node (`dist/`), Go (static binary) and other templates — the artifact
  pipeline is language-agnostic; only the wrapper template changes. Next
  after Java.
- The registry route (above), remote builders, build caches, `GRADLE_OPTS`
  / `NODE_OPTIONS` build env (separate backlog items).
- Reconcile polling, token-expiry warnings, base-image digest pinning.

## As built — P1 deviations from the design above

- **Idempotency ignores failed deployments.** The design said "skip if a
  `ci` deployment with the same `ci_run` exists"; but GitHub's *Re-run*
  keeps the run id, so counting a failed deployment would make a failed
  deploy unretryable. `HasCIRun` and `LatestCIRunNumber` both count only
  queued/building/running deployments. (Found while writing the gate
  tests; unit- and e2e-covered.)
- **Extra gate:** `repository.full_name` in the payload must equal the
  source's own repo (when its URL parses as owner/name).
- **Response bodies are fixed strings** (`ignored: a different workflow`,
  `ignored: the run is from a fork`, …), never payload text.
- **Seeding:** until P2, `seed-git-source` takes `DEPLOYMATE_SEED_MODE`,
  `_API_TOKEN`, `_WORKFLOW`, `_ARTIFACT` from the environment (the CLI
  argument list is unchanged).
- **Wrapper:** `ENTRYPOINT … -Dserver.port=${PORT:-8080}` (the platform
  always injects `PORT`; the default only matters if run by hand);
  `JavaMajor` maps `java:21.0.2` → `21`, anything non-Java → the default
  21, and only 1–2 digit majors ever reach the `FROM` line.
- **Worker seams:** `Worker.SetGitHubAPI` (the fake GitHub) and
  `Worker.buildFn` (no Docker daemon in unit tests).
- **Webhook response when a source has only prebuilt apps and a push
  arrives:** `ignored: this app deploys from CI runs, not pushes`.
- **Verified end to end** (`make e2e-artifact`): gates; v1 → run → serving
  non-root with the `JAVA_OPTS` default; nothing cloned and the working
  directory cleaned; the token never reaching the storage host; replay and
  stale-run refusal; v2; seven failure causes with v2 serving throughout;
  retry of a failed run id; **rollback with GitHub unreachable**; a
  build-mode app untouched by CI events; no secret in the server log or any
  build log.

## As built — P2 (UI)

- **Where:** the app page's Git panel, for GitHub sources only
  (`GitInfo.GitHub`); GitLab/Gitea apps never see it and
  `POST …/deploy-mode` refuses them. Build mode is the default and its
  panel is unchanged ("Review & deploy…").
- **Routes** (all CSRF-protected): `POST /apps/{slug}/deploy-mode`,
  `POST /apps/{slug}/git/test`, `POST /apps/{slug}/git/deploy-latest`
  (`internal/httpserver/handlers_prebuilt.go`).
- **Save rules:** prebuilt requires a stored token; workflow path must match
  `.github/workflows/<name>.yml|yaml`; artifact name `[A-Za-z0-9._-]{1,100}`;
  a token is trimmed, ≤255 chars, no whitespace. The token field is
  `type=password`, never pre-filled; a blank field **keeps** the stored token
  and "Remove saved token" clears it. The token is stored encrypted and is
  never rendered, logged or echoed in a flash message (tested, and checked by
  `make e2e-artifact`).
- **Test connection** (saved token + saved workflow): repo reachable →
  workflow runs readable (a 404 here means "workflow file not in the repo
  yet") → counts **other private repos** the token can read and warns when
  any (`⚠ … recreate it with Only select repositories`). GitHub failures map
  to fixed sentences (401 wrong/expired, 403 needs *Actions: read-only* or org
  approval, 404 not found or not granted); GitHub's own message is not echoed.
- **Deploy latest successful run** lists the newest 10 successful runs of the
  workflow on the tracked branch and takes the first that passes **the same
  `ciSkipReason` gates as a webhook** (fork, event, already-handled,
  older-than-deployed all refused), queueing a `deploy` with
  `trigger = dashboard` and the run's id/number/commit. A repeat press
  reports "already handled" instead of deploying an older run. This is also the
  first-deploy path and the answer to a missed webhook.
- **Review page:** `POST /git/deploy` on a prebuilt app delegates to the same
  action; the page's button reads "Deploy latest successful run".
- **Generated workflow** (`githubci.Workflow`): Gradle and Maven variants,
  branch/Java major/artifact name filled in (branch is always emitted as a
  quoted YAML string), `workflow_dispatch` included so "Run workflow" works,
  `retention-days: 1`, the single-`out/app.jar` step. Gradle multi-module
  projects must edit the task (e.g. `:app:bootJar`) — noted on the page.
- **Verified:** unit tests (`handlers_prebuilt_test.go`, `workflow_test.go`),
  and `make e2e-artifact` now also drives the whole UI flow against the fake
  GitHub (refuse without token → save → token encrypted/never rendered → Test
  connection with the scope warning → Deploy latest skips a fork's run and
  deploys → repeat is a no-op); layout checked in the browser pane.
