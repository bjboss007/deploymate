# 0019 — Prebuilt deploys: CI builds the JAR, DeployMate wraps and runs it

- **Date:** 2026-10-02
- **Status:** accepted (P1 + P2 implemented: data model, webhook gates, GitHub
  client, worker, e2e, and the dashboard UI — see docs/specs/prebuilt-deploys.md;
  P3 memory preflight is open)

## Context

DeployMate built every git app on the server that serves traffic. A JVM
build (Gradle daemon + compilers + BuildKit) peaks at ~2.5–3 GB while the
app *runs* in ~0.3–0.6 GB, so a 2–4 GB server can host what it can never
build (found live: the `testing` app, `ResourceExhausted … cannot allocate
memory`). Owner constraints: **no builder machine, no container registry**.

## Decision

Add an **opt-in** per-app deploy mode, `apps.deploy_mode = 'artifact'`
(default `build` — every existing app and code path is unchanged):

1. **CI builds, DeployMate runs.** GitHub Actions compiles and uploads the
   JAR as an artifact; GitHub's `workflow_run` event arrives on the app's
   **existing** webhook; the worker downloads the artifact, takes the one
   JAR out, wraps it in `eclipse-temurin:{ver}-jre` (`COPY` only — 1.4 s,
   no measurable extra memory, spike S2), and runs the usual rolling,
   replica-aware swap. Rollback, image pruning, stats and the releases page
   work unchanged because the wrapped image is recorded like any built one.
2. **Six gates on the `workflow_run`** (webhook, after HMAC): completed +
   success; the app's own workflow file (`apps.workflow_path`); the
   tracked branch; trigger is `push` or `workflow_dispatch` — **never**
   `pull_request` (a fork PR branch named like the deploy branch would
   otherwise deploy attacker-built bytes); same-repo head (not a fork) and
   `repository.full_name` matches the source; one live deployment per run
   id and never a run no newer than what is deployed or in flight
   (`deployments.ci_run`, `ci_run_number`). **Failed deployments do not
   count** for either check — GitHub's "Re-run" keeps the run id, which is
   exactly how a failed deploy is retried.
3. **A separate credential:** `git_sources.api_token_enc`, a **fine-grained
   GitHub token with `Actions: read`** (verified: lists runs, lists
   artifacts, downloads — and nothing else; contents and workflow dispatch
   are 403). Not `pat_enc`: that is an (unused) *clone* credential, and
   conflating them would make a future HTTPS-clone feature ambiguous.
4. **Credential safety in the download:** GitHub answers with a 302 to a
   signed blob URL (valid ~1 minute, on a different host); it is fetched by
   a clean client with **no `Authorization` header**, and the URL is never
   logged or put in an error. Asserted in unit tests (the storage fake
   rejects any request carrying a token) and in the e2e.
5. **Unzip by position, not by name:** exactly one `*.jar` (not
   `-plain.jar`) is copied from the zip to a fixed destination chosen by the
   caller; entry names never form a path, directories/symlinks are ignored,
   the copy is size-capped, entry count is capped. Zero or several JARs is
   an error that lists them — DeployMate never guesses which is the app.
6. **Every failure names its cause** (token rejected / refused / repo not
   found; no artifact of that name; expired; digest mismatch; not a zip; no
   or several JARs; wrapper build failure via ADR 0015's diagnosis) and
   **happens before the swap**, so the previous version keeps serving and
   the app is never marked failed by one.
7. **Test seams, not test modes:** `DEPLOYMATE_GITHUB_API_URL` (test only)
   points the worker at a fake GitHub (`testdata/fakegithub.py`, mirroring
   the response shapes captured from real GitHub); the worker's `buildFn`
   replaces `docker buildx` in unit tests.

## Consequences

- A 2 GB server can run apps it can never build; the build moves to GitHub's
  runners (Actions minutes, and artifact storage — mitigated by
  `retention-days: 1`; the wrapped image is DeployMate's own copy, so GitHub
  expiring the artifact never affects rollback).
- A webhook delivery that GitHub fails to deliver is not retried by GitHub;
  with 1-day retention the artifact can expire first. P2's "Deploy latest
  successful run" button is the recovery path (and the first-deploy path).
- The token's repository scope is the owner's choice at creation; spike
  S1-B showed an *All repositories* token reads Actions artifacts of every
  private repo. DeployMate can only warn (P2: count of *private* repos other
  than this one — public repos are always readable by any token).
- Prebuilt apps ignore pushes (the artifact does not exist yet); build-mode
  apps ignore CI events. Both can share one git source and each follows its
  own mode.
- Java only in v1 (the wrapper template); Node/Go/static are new templates
  on the same pipeline.

## Alternatives

- **Registry route (GHCR image):** the owner has no registry; private pulls
  need a *classic* account-wide `read:packages` token (fine-grained tokens
  are not supported for the container registry), and CI must build for the
  server's architecture. Documented as v2 on the same trigger/mode seam.
- **Remote builder** (`BUILDKIT_HOST` → a bigger machine): no builder
  machine exists.
- **Leaner on-box builds** (`GRADLE_OPTS`, no daemon): still ~1–1.5 GB peak;
  a 2 GB server cannot build a JVM app. Backlogged as a complement, plus a
  memory-preflight advisory (P3).
- **CI pushes the JAR to DeployMate:** the Cloudflare tunnel caps request
  bodies at 100 MB, and a fat JAR can exceed it; pull-based download has no
  such limit and needs no inbound auth beyond the existing webhook.
