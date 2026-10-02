#!/usr/bin/env bash
# DeployMate prebuilt-deploy e2e (docs/specs/prebuilt-deploys.md), against a
# THROWAWAY server and a FAKE GitHub (testdata/fakegithub.py) so it never
# touches the live :8090 apps or the real GitHub.
#
#   an app in "artifact" mode (seed-git-source + DEPLOYMATE_SEED_* env)
#   signed workflow_run webhooks, through every gate
#     -> the worker downloads the run's artifact from the fake GitHub
#        (302 to a separate storage host that rejects any Authorization),
#        takes the one JAR, wraps it in eclipse-temurin:21-jre, and runs it
#     -> /preview serves the fixture jar's marker
#   v2 deploy, stale/duplicate/fork/PR runs ignored, 7 failure messages with
#   the previous version still serving, retry of a failed run, rollback with
#   GitHub DOWN, and a build-mode app that no CI event can touch.
#
# Everything is cleaned up on exit. Slugs are timestamped so container names
# can never match the user's real containers.
#
#   ./testdata/e2e_artifact.sh        # builds ./bin/deploymate first
#   DM_PORT=18102 ./testdata/e2e_artifact.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DM_BIN:-$ROOT/bin/deploymate}"
PORT="${DM_PORT:-18102}"
GH_API="${DM_GH_API_PORT:-18103}"
GH_BLOB="${DM_GH_BLOB_PORT:-18104}"
BASE="http://127.0.0.1:$PORT"
EMAIL="owner@e2e.dev"
PASSWORD="e2epassword123"
STAMP="$(date +%s)"
SLUG="e2eart$STAMP"
SLUG2="e2eartb$STAMP"            # a build-mode app on its own source
REPO="acme/web"
TOKEN="e2e-fake-github-token-$STAMP"
SHA="0123456789abcdef0123456789abcdef01234567"
MARKER="deploymate e2e prebuilt jar fixture"
WF=".github/workflows/deploymate.yml"

DATA_DIR="$(mktemp -d)"
JAR="$(mktemp)"
SRV_PID=""
GH_PID=""

log()  { echo "== $*"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null && wait "$SRV_PID" 2>/dev/null
  [ -n "$GH_PID" ] && kill "$GH_PID" 2>/dev/null
  for c in $(docker ps -aq --filter "name=^dm-$SLUG" --filter "name=^dm-$SLUG2" --filter "name=^dm-${SLUG3:-none}" 2>/dev/null); do
    docker rm -f "$c" >/dev/null 2>&1
  done
  for c in $(docker ps -aq --filter "name=^dm-e2eart" 2>/dev/null); do
    case "$(docker inspect -f '{{.Name}}' "$c" 2>/dev/null)" in /dm-$SLUG*|/dm-$SLUG2*|/dm-${SLUG3:-none}*) docker rm -f "$c" >/dev/null 2>&1 ;; esac
  done
  imgs="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E "^deploymate/apps/($SLUG|$SLUG2|${SLUG3:-none}):" 2>/dev/null)"
  [ -n "$imgs" ] && docker rmi -f $imgs >/dev/null 2>&1
  rm -rf "$DATA_DIR" "$JAR"
}
trap cleanup EXIT

if [ -z "${DM_BIN:-}" ]; then
  log "building binary"
  make -C "$ROOT" build >/dev/null
fi
[ -x "$BIN" ] || fail "binary not found at $BIN"
command -v python3 >/dev/null || fail "python3 is required for the fake GitHub"
docker image inspect eclipse-temurin:21-jre >/dev/null 2>&1 || { log "pulling eclipse-temurin:21-jre (once)"; docker pull -q eclipse-temurin:21-jre >/dev/null; }

# 1. Fake GitHub + throwaway server (pointed at it). No Cloudflare vars.
# Refuse to run over a stale process: a leftover fake on these ports would
# answer with ITS token and fail this run for the wrong reason.
for p in "$PORT" "$GH_API" "$GH_BLOB"; do
  ! lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 || fail "port $p is already in use (a stale server or fake GitHub?) — free it or set DM_PORT / DM_GH_API_PORT / DM_GH_BLOB_PORT"
done
python3 "$ROOT/testdata/fakegithub.py" "$GH_API" "$GH_BLOB" "$ROOT/testdata/apps/hellojar/hello.jar" "$TOKEN" "$REPO" >"$DATA_DIR/fakegh.log" 2>&1 &
GH_PID=$!
for i in $(seq 1 30); do curl -sf -o /dev/null "http://127.0.0.1:$GH_API/__stats" && break; sleep 0.2; [ "$i" = 30 ] && fail "fake GitHub never came up"; done
kill -0 "$GH_PID" 2>/dev/null || { cat "$DATA_DIR/fakegh.log"; fail "fake GitHub exited during startup"; }
log "starting throwaway server on $PORT (fake GitHub on $GH_API, data $DATA_DIR)"
env -i PATH="$PATH" HOME="$HOME" \
  DEPLOYMATE_ADDR="127.0.0.1:$PORT" \
  DEPLOYMATE_DATA_DIR="$DATA_DIR" \
  DEPLOYMATE_SETUP_EMAIL="$EMAIL" \
  DEPLOYMATE_SETUP_PASSWORD="$PASSWORD" \
  DEPLOYMATE_GITHUB_API_URL="http://127.0.0.1:$GH_API" \
  "$BIN" serve >"$DATA_DIR/server.log" 2>&1 &
SRV_PID=$!
for i in $(seq 1 50); do
  curl -sf -o /dev/null "$BASE/login" && break
  kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server exited during startup"; }
  sleep 0.2
  [ "$i" = 50 ] && { cat "$DATA_DIR/server.log"; fail "server never became ready"; }
done

curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" || fail "login"
csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }
db() { sqlite3 "$DATA_DIR/data.db" "$1" 2>/dev/null || true; }

CSRF="$(csrf "$BASE/projects")"
PROJ="e2e-$STAMP"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
for s in "$SLUG" "$SLUG2"; do
  curl -sf -b "$JAR" -o /dev/null -d "name=$s&csrf_token=$CSRF" "$BASE/projects/$PROJ/apps" || fail "create app $s"
done

# The prebuilt app: artifact mode + an (encrypted) API token, via the seed hook.
seed="$(env DEPLOYMATE_DATA_DIR="$DATA_DIR" DEPLOYMATE_SEED_MODE=artifact DEPLOYMATE_SEED_API_TOKEN="$TOKEN" \
  "$BIN" seed-git-source "$SLUG" "https://github.com/$REPO.git" main github 2>/dev/null)" || fail "seed prebuilt app"
SRC="$(echo "$seed" | grep '^source_id=' | cut -d= -f2)"; SECRET="$(echo "$seed" | grep '^webhook_secret=' | cut -d= -f2)"
# A build-mode app on its own source for the same repo (no mode env).
seed2="$(env DEPLOYMATE_DATA_DIR="$DATA_DIR" "$BIN" seed-git-source "$SLUG2" "https://github.com/$REPO.git" main github 2>/dev/null)" || fail "seed build-mode app"
SRC2="$(echo "$seed2" | grep '^source_id=' | cut -d= -f2)"; SECRET2="$(echo "$seed2" | grep '^webhook_secret=' | cut -d= -f2)"
[ -n "$SRC" ] && [ -n "$SRC2" ] && [ "$SRC" != "$SRC2" ] || fail "seeding failed"
[ "$(db "SELECT deploy_mode FROM apps WHERE slug='$SLUG'")" = "artifact" ] || fail "app is not in artifact mode"
[ "$(db "SELECT deploy_mode FROM apps WHERE slug='$SLUG2'")" = "build" ] || fail "second app must stay in build mode"
log "apps ready: $SLUG (artifact) and $SLUG2 (build)"

hook() { # hook <source> <secret> <event> <body> → "<response> <code>"   (fresh delivery GUID each call —
         # random, because this runs in a $(...) subshell where a counter would not persist)
  local guid; guid="e2e-art-$STAMP-$(openssl rand -hex 6)"
  local sig; sig="sha256=$(printf '%s' "$4" | openssl dgst -sha256 -hmac "$2" | awk '{print $NF}')"
  curl -s -w ' %{http_code}' -H "X-GitHub-Event: $3" -H "X-GitHub-Delivery: $guid" \
    -H "X-Hub-Signature-256: $sig" -H "Content-Type: application/json" -d "$4" "$BASE/hooks/$1"
}
wr() { # wr ACTION CONCLUSION PATH EVENT BRANCH HEADREPO RUNID NUMBER
  printf '{"action":"%s","workflow_run":{"id":%s,"name":"DeployMate build","path":"%s","event":"%s","status":"completed","conclusion":"%s","head_branch":"%s","head_sha":"%s","run_number":%s,"run_attempt":1,"head_commit":{"message":"e2e prebuilt"},"head_repository":{"full_name":"%s"}},"repository":{"full_name":"%s"}}' \
    "$1" "$7" "$3" "$4" "$2" "$5" "$SHA" "$8" "$6" "$REPO"
}
good() { wr completed success "$WF" push main "$REPO" "$1" "$2"; } # good RUNID NUMBER
expect() { # expect <label> <want> <got>
  [ "$3" = "$2" ] || fail "$1: got '$3', want '$2'"
}
dep_status() { db "SELECT status FROM deployments WHERE ci_run=$1 ORDER BY created_at DESC LIMIT 1"; }
dep_id()     { db "SELECT id FROM deployments WHERE ci_run=$1 ORDER BY created_at DESC LIMIT 1"; }
wait_dep() { # wait_dep <run> <status> <secs>
  local st=""
  for _ in $(seq 1 "$3"); do
    st="$(dep_status "$1")"; [ "$st" = "$2" ] && return 0
    kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server died"; }
    sleep 1
  done
  echo "run $1 last status: '$st' error: $(db "SELECT error FROM deployments WHERE ci_run=$1 ORDER BY created_at DESC LIMIT 1")" >&2
  return 1
}
serves() { curl -s -b "$JAR" "$BASE/preview/$SLUG/" | grep -q "$MARKER"; }

# 2. Gates: every refusal has its reason and queues nothing.
log "gates"
expect "ping" "pong 200" "$(hook "$SRC" "$SECRET" ping '{"zen":"e2e"}')"
expect "requested run" "ignored: workflow run is not completed 200" "$(hook "$SRC" "$SECRET" workflow_run "$(wr requested success "$WF" push main "$REPO" 1001 1)")"
expect "failed run" "ignored: workflow run did not succeed 200" "$(hook "$SRC" "$SECRET" workflow_run "$(wr completed failure "$WF" push main "$REPO" 1001 1)")"
expect "other workflow" "ignored: a different workflow 200" "$(hook "$SRC" "$SECRET" workflow_run "$(wr completed success .github/workflows/lint.yml push main "$REPO" 1001 1)")"
expect "other branch" "ignored: not the deploy branch 200" "$(hook "$SRC" "$SECRET" workflow_run "$(wr completed success "$WF" push feature "$REPO" 1001 1)")"
expect "pull_request run" "ignored: only push and manual runs deploy 200" "$(hook "$SRC" "$SECRET" workflow_run "$(wr completed success "$WF" pull_request main "$REPO" 1001 1)")"
expect "fork run" "ignored: the run is from a fork 200" "$(hook "$SRC" "$SECRET" workflow_run "$(wr completed success "$WF" push main mallory/web 1001 1)")"
expect "push (prebuilt app)" "ignored: this app deploys from CI runs, not pushes 200" "$(hook "$SRC" "$SECRET" push '{"ref":"refs/heads/main","after":"abc","head_commit":{"message":"x"}}')"
[ "$(db "SELECT COUNT(*) FROM deployments")" = "0" ] || fail "a refused event queued a deployment"
log "PASS: gates refuse with reasons and queue nothing"

# 3. Deploy v1.
expect "run 1001" "queued 200" "$(hook "$SRC" "$SECRET" workflow_run "$(good 1001 1)")"
wait_dep 1001 running 120 || fail "run 1001 never reached running"
D1="$(dep_id 1001)"; IMG1="deploymate/apps/$SLUG:$D1"
[ "$(db "SELECT trigger FROM deployments WHERE id='$D1'")" = "ci" ] || fail "deployment trigger must be ci"
for i in $(seq 1 30); do serves && break; sleep 1; [ "$i" = 30 ] && fail "the prebuilt jar never served through /preview"; done
[ "$(docker inspect -f '{{.Config.Image}}' "dm-$SLUG")" = "$IMG1" ] || fail "container is not running the wrapped image"
[ "$(docker exec "dm-$SLUG" id -un)" = "app" ] || fail "the wrapper must run as the non-root user"
docker exec "dm-$SLUG" sh -c 'echo "$JAVA_OPTS"' | grep -q "MaxRAMPercentage=75" || fail "JAVA_OPTS default missing"
[ -z "$(ls -A "$DATA_DIR/repos" 2>/dev/null)" ] || fail "a prebuilt deploy must not clone anything"
[ -z "$(ls -A "$DATA_DIR/builds" 2>/dev/null)" ] || fail "the working directory must be cleaned up"
curl -s "http://127.0.0.1:$GH_API/__stats" | grep -q '"blob_auth_seen": false' || fail "the token reached the storage host across the redirect"
log "PASS: run 1001 → artifact downloaded, wrapped, running non-root, serving; nothing cloned; token never crossed the redirect"

# 4. Duplicates / stale runs / v2.
expect "replayed run" "ignored: this run was already handled 200" "$(hook "$SRC" "$SECRET" workflow_run "$(good 1001 1)")"
expect "run 1002" "queued 200" "$(hook "$SRC" "$SECRET" workflow_run "$(good 1002 2)")"
wait_dep 1002 running 120 || fail "run 1002 never reached running"
D2="$(dep_id 1002)"; IMG2="deploymate/apps/$SLUG:$D2"
[ "$IMG1" != "$IMG2" ] && [ "$(docker inspect -f '{{.Config.Image}}' "dm-$SLUG")" = "$IMG2" ] || fail "v2 must run its own image"
serves || fail "v2 stopped serving"
expect "stale run" "ignored: a newer run is already deployed 200" "$(hook "$SRC" "$SECRET" workflow_run "$(good 1000 1)")"
log "PASS: replays and stale runs ignored; v2 deployed ($IMG2)"

# 5. Failures: each names its cause and the running version keeps serving.
fails() { # fails <run> <number> <substring>
  expect "run $1" "queued 200" "$(hook "$SRC" "$SECRET" workflow_run "$(good "$1" "$2")")"
  wait_dep "$1" failed 60 || fail "run $1 should have failed"
  local err; err="$(db "SELECT error FROM deployments WHERE ci_run=$1 ORDER BY created_at DESC LIMIT 1")"
  echo "$err" | grep -qF "$3" || fail "run $1 error '$err' does not mention '$3'"
  serves || fail "run $1 failed but the previous version stopped serving"
  [ "$(docker inspect -f '{{.Config.Image}}' "dm-$SLUG")" = "$IMG2" ] || fail "run $1 failed but the running image changed"
}
fails 1003 3 "has expired"
fails 1004 4 "uploaded no artifact named"
fails 1005 5 "integrity check"
fails 1006 6 "exactly one .jar"
fails 1007 7 "no .jar found"
fails 1008 8 "not a valid zip"
fails 1099 9 "could not find $REPO"
[ "$(db "SELECT status FROM apps WHERE slug='$SLUG'")" = "running" ] || fail "failed deploys must not mark the app failed"
log "PASS: 7 failure causes named; v2 never stopped serving"

# A failed run can be retried: GitHub's Re-run re-delivers the same run id.
expect "retry of failed run" "queued 200" "$(hook "$SRC" "$SECRET" workflow_run "$(good 1003 3)")"
log "PASS: a failed run id is retryable"
grep -q "SECRETSIGNATURE\|$TOKEN" "$DATA_DIR/server.log" && fail "a secret leaked into the server log"
[ "$(db "SELECT COUNT(*) FROM build_logs WHERE line LIKE '%SECRETSIGNATURE%' OR line LIKE '%$TOKEN%'")" = "0" ] || fail "a secret leaked into a build log"

# 5b. The UI flow (P2): an app on a build-mode source is switched to prebuilt
# through the dashboard forms, tested, and deployed with "Deploy latest
# successful run" — no hand-signed webhook, no seeding of mode or token.
log "UI flow"
SLUG3="e2eartc$STAMP"
CSRF="$(csrf "$BASE/projects")"
curl -sf -b "$JAR" -o /dev/null -d "name=$SLUG3&csrf_token=$CSRF" "$BASE/projects/$PROJ/apps" || fail "create app $SLUG3"
env DEPLOYMATE_DATA_DIR="$DATA_DIR" "$BIN" seed-git-source "$SLUG3" "https://github.com/$REPO.git" main github >/dev/null 2>&1 || fail "seed $SLUG3"
flash() { python3 -c 'import sys,urllib.parse as u; print(u.parse_qs(u.urlparse(sys.argv[1]).query).get("flash",[""])[0])' "$1"; }
CSRF="$(csrf "$BASE/apps/$SLUG3")"
page="$(curl -s -b "$JAR" "$BASE/apps/$SLUG3")"
echo "$page" | grep -q 'Build on this server' || fail "mode select missing on a GitHub-source app"
echo "$page" | grep -q 'Deploy latest successful run' && fail "prebuilt buttons shown in build mode"
loc="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "mode=artifact&csrf_token=$CSRF" "$BASE/apps/$SLUG3/deploy-mode")"
flash "$loc" | grep -q "needs a GitHub token" || fail "prebuilt without a token must be refused ($(flash "$loc"))"
curl -s -b "$JAR" -o /dev/null --data-urlencode "mode=artifact" --data-urlencode "api_token=$TOKEN" -d "csrf_token=$CSRF" "$BASE/apps/$SLUG3/deploy-mode"
[ "$(db "SELECT deploy_mode FROM apps WHERE slug='$SLUG3'")" = "artifact" ] || fail "UI did not switch the app to prebuilt"
[ -n "$(db "SELECT api_token_enc FROM git_sources s JOIN apps a ON a.git_source_id=s.id WHERE a.slug='$SLUG3'")" ] || fail "token not stored"
db "SELECT api_token_enc FROM git_sources" | grep -qF "$TOKEN" && fail "token stored in plaintext"
page="$(curl -s -b "$JAR" "$BASE/apps/$SLUG3")"
echo "$page" | grep -qF "$TOKEN" && fail "the token is rendered on the page"
for want in 'Deploy latest successful run' 'Test connection' 'retention-days: 1' 'saved — leave blank to keep'; do
  echo "$page" | grep -qF "$want" || fail "prebuilt panel lacks '$want'"
done
loc="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "csrf_token=$CSRF" "$BASE/apps/$SLUG3/git/test")"
f="$(flash "$loc")"
echo "$f" | grep -q "Connected to $REPO" && echo "$f" | grep -q "2 other private repositories" || fail "Test connection: '$f'"
loc="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "csrf_token=$CSRF" "$BASE/apps/$SLUG3/git/deploy-latest")"
D3="${loc##*/deployments/}"; [ -n "$D3" ] && [ "$D3" != "$loc" ] || fail "deploy-latest did not queue ($(flash "$loc"))"
[ "$(db "SELECT ci_run FROM deployments WHERE id='$D3'")" = "1002" ] || fail "deploy-latest must skip the fork's run and take 1002"
for i in $(seq 1 120); do
  st="$(db "SELECT status FROM deployments WHERE id='$D3'")"; [ "$st" = running ] && break
  [ "$st" = failed ] && fail "UI deploy failed: $(db "SELECT error FROM deployments WHERE id='$D3'")"
  sleep 1; [ "$i" = 120 ] && fail "UI deploy never reached running"
done
loc="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "csrf_token=$CSRF" "$BASE/apps/$SLUG3/git/deploy-latest")"
flash "$loc" | grep -q "already handled" || fail "second press must say already handled ($(flash "$loc"))"
log "PASS: UI — switch to prebuilt, token encrypted and never rendered, Test connection (+scope warning), Deploy latest skips the fork run, repeat is a no-op"

# 6. Rollback with GitHub DOWN: the kept local image is all it needs.
kill "$GH_PID" 2>/dev/null; wait "$GH_PID" 2>/dev/null || true; GH_PID=""
sleep 1
out="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "csrf_token=$(csrf "$BASE/apps/$SLUG")" "$BASE/deployments/$D1/rollback")"
RB="${out##*/deployments/}"; [ -n "$RB" ] || fail "rollback was not queued ($out)"
for i in $(seq 1 60); do
  [ "$(db "SELECT status FROM deployments WHERE id='$RB'")" = "running" ] && break
  sleep 1; [ "$i" = 60 ] && fail "rollback never reached running: $(db "SELECT error FROM deployments WHERE id='$RB'")"
done
[ "$(docker inspect -f '{{.Config.Image}}' "dm-$SLUG")" = "$IMG1" ] || fail "rollback is not running v1's image"
for i in $(seq 1 30); do serves && break; sleep 1; [ "$i" = 30 ] && fail "rolled-back app does not serve"; done
log "PASS: rollback to v1 worked with GitHub unreachable"

# 7. A build-mode app is untouched by CI events.
expect "build-mode app" "ignored: no app on this source deploys from CI runs 200" "$(hook "$SRC2" "$SECRET2" workflow_run "$(good 1001 1)")"
[ "$(db "SELECT COUNT(*) FROM deployments d JOIN apps a ON a.id=d.app_id WHERE a.slug='$SLUG2'")" = "0" ] || fail "a CI event deployed a build-mode app"
log "PASS: build-mode app untouched by CI events"

echo "PASS: artifact e2e"
