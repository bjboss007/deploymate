# Prebuilt deploys (CI builds, DeployMate runs) — specification

**Status:** design (Oct 2026). **P0 shipped 2026-10-02** (per-source
delivery key + `X-GitHub-Event` dispatch); everything else **not
implemented**. Owner constraints:
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
      - uses: actions/upload-artifact@v4
        with:
          name: deploymate-app
          path: out/app.jar
          retention-days: 1          # keeps shared Actions/Packages storage near zero
          if-no-files-found: error
```
The `cp … out/app.jar` step is what makes multi-module Gradle builds safe:
the artifact always holds exactly one file, so DeployMate never has to
guess which JAR is the application.

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

- **S1 — real GitHub round trip.** A throwaway private repo with the
  generated workflow: confirm the `workflow_run` delivery reaches an
  existing DeployMate webhook and its exact payload fields (incl.
  `head_repository`, `run_number`, `run_attempt`); that a fine-grained token
  with only *Actions: read* lists and downloads the artifact; that the
  redirect works with Go's client; whether `digest` is populated. **Then
  repeat under the `BeyondCredit` org** — org policy may require approval or
  SSO authorization for fine-grained tokens (owner action).
- **S2 — wrapper cost on the target box.** `docker buildx build` of the
  2-line Dockerfile on the 4 GB laptop server: wall time, peak RAM; first
  `eclipse-temurin:21-jre` pull time and Docker Hub anonymous-limit
  behavior.
- **S3 — retention accounting.** Confirm `retention-days: 1` keeps an
  80 MB-JAR-per-push workflow comfortably under the Free/Pro shared quota
  and how fast storage is reclaimed (the docs fetched don't say).

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
GitHub API server + a committed few-KB fixture JAR (a `com.sun.net.httpserver`
hello-world) → a signed `workflow_run` webhook → worker downloads from the
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
2. **P1** — spikes S1–S3 → migration 0017, GitHub client, `runArtifactDeploy`,
   template, gates, failure messages, `make e2e-artifact`.
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
