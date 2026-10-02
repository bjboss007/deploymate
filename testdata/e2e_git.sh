#!/usr/bin/env bash
# DeployMate git-path e2e: the real product flow, end to end, against a
# THROWAWAY server so it never touches the user's live :8090 apps.
#
#   spare port + scratch data dir  →  fresh owner + project + app
#   local bare repo (from testdata/repos/e2e-web fixture)
#   seed-git-source links the app to it (bypasses the ssh/https-only UI gate)
#   signed GitHub webhook  →  worker clones, builds the Dockerfile, swaps in
#   poll the container to Running  →  HTTP-probe it through /preview/{slug}/
#
# Everything is cleaned up on exit (server, container, built image, temp dirs).
# The Docker daemon is shared with the user's real server, so the app slug is
# timestamped to guarantee a unique container name / preview port / image.
#
#   ./testdata/e2e_git.sh            # builds ./bin/deploymate first via `make build` if absent
#   DM_PORT=18091 ./testdata/e2e_git.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DM_BIN:-$ROOT/bin/deploymate}"
PORT="${DM_PORT:-18091}"
BASE="http://127.0.0.1:$PORT"
EMAIL="owner@e2e.dev"
PASSWORD="e2epassword123"
STAMP="$(date +%s)"
APP_NAME="e2eweb$STAMP"          # slug == name (lowercase, no spaces)
SLUG="$APP_NAME"
CONTAINER="dm-$SLUG"
IMAGE_PREFIX="deploymate/apps/$SLUG"
# Second app on the SAME repo/branch (the dev/stage/prod-on-one-repo shape):
# its own git source + webhook, same push GUID from GitHub.
SLUG2="${APP_NAME}b"
CONTAINER2="dm-$SLUG2"
IMAGE_PREFIX2="deploymate/apps/$SLUG2"

DATA_DIR="$(mktemp -d)"
BARE="$(mktemp -d)/e2e-web.git"
WORK="$(mktemp -d)"
JAR="$(mktemp)"
SRV_PID=""

log()  { echo "== $*"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null && wait "$SRV_PID" 2>/dev/null
  docker rm -f "$CONTAINER" "$CONTAINER2" >/dev/null 2>&1
  # Remove only THIS run's built images (slugs are timestamped).
  imgs="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E "^($IMAGE_PREFIX|$IMAGE_PREFIX2):" 2>/dev/null)"
  [ -n "$imgs" ] && docker rmi -f $imgs >/dev/null 2>&1
  rm -rf "$DATA_DIR" "$BARE" "$WORK" "$JAR" "$(dirname "$BARE")"
}
trap cleanup EXIT

# 0. Binary — build fresh so the suite always tests current code (unless the
#    caller pins a prebuilt one via DM_BIN).
if [ -z "${DM_BIN:-}" ]; then
  log "building binary"
  make -C "$ROOT" build >/dev/null
fi
[ -x "$BIN" ] || fail "binary not found at $BIN"

# 1. Local bare repo from the fixture
log "building bare repo from testdata/repos/e2e-web"
cp -R "$ROOT/testdata/repos/e2e-web/." "$WORK/"
git -C "$WORK" init -q -b main
git -C "$WORK" -c user.email=e2e@dm -c user.name=e2e add -A
git -C "$WORK" -c user.email=e2e@dm -c user.name=e2e commit -qm "e2e fixture"
git clone -q --bare "$WORK" "$BARE"
SHA="$(git --git-dir="$BARE" rev-parse main)"
log "fixture HEAD $SHA"

# 2. Start a throwaway server (fresh owner via setup env). Deliberately NO
#    Cloudflare vars → auto-DNS stays off, no real DNS records created.
log "starting throwaway server on $PORT (data $DATA_DIR)"
env -i PATH="$PATH" HOME="$HOME" \
  DEPLOYMATE_ADDR="127.0.0.1:$PORT" \
  DEPLOYMATE_DATA_DIR="$DATA_DIR" \
  DEPLOYMATE_SETUP_EMAIL="$EMAIL" \
  DEPLOYMATE_SETUP_PASSWORD="$PASSWORD" \
  "$BIN" serve >"$DATA_DIR/server.log" 2>&1 &
SRV_PID=$!

for i in $(seq 1 50); do
  curl -sf -o /dev/null "$BASE/login" && break
  kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server exited during startup"; }
  sleep 0.2
  [ "$i" = 50 ] && { cat "$DATA_DIR/server.log"; fail "server never became ready"; }
done
log "server ready"

# 3. Login + create project and app
curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" || fail "login"
csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }

CSRF="$(csrf "$BASE/projects")"
PROJ="e2e-$STAMP"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
PROJ_SLUG="$(echo "$PROJ" | tr '[:upper:]' '[:lower:]')"
curl -sf -b "$JAR" -o /dev/null -d "name=$APP_NAME&csrf_token=$CSRF" "$BASE/projects/$PROJ_SLUG/apps" || fail "create app"
log "project + app $SLUG created"

# 4. Link the app to the local bare repo (seed-git-source prints the secret)
seed_out="$(env DEPLOYMATE_DATA_DIR="$DATA_DIR" "$BIN" seed-git-source "$SLUG" "$BARE" main github)" \
  || fail "seed-git-source"
SOURCE_ID="$(echo "$seed_out" | grep '^source_id=' | cut -d= -f2)"
SECRET="$(echo "$seed_out" | grep '^webhook_secret=' | cut -d= -f2)"
[ -n "$SOURCE_ID" ] && [ -n "$SECRET" ] || fail "seed output missing (got: $seed_out)"
log "git source $SOURCE_ID linked"

# 5. Signed GitHub push webhook → queues a real worker deploy at $SHA
BODY="{\"ref\":\"refs/heads/main\",\"after\":\"$SHA\",\"head_commit\":{\"message\":\"e2e fixture\"}}"
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $NF}')"
resp="$(curl -s -w '\n%{http_code}' \
  -H "X-GitHub-Event: push" \
  -H "X-GitHub-Delivery: e2e-$STAMP" \
  -H "X-Hub-Signature-256: $SIG" \
  -H "Content-Type: application/json" \
  -d "$BODY" "$BASE/hooks/$SOURCE_ID")"
code="$(echo "$resp" | tail -1)"
[ "$code" = "200" ] || fail "webhook returned $code: $resp"
echo "$resp" | grep -q queued || fail "webhook not queued: $resp"
log "webhook accepted, deploy queued"

# 5b. Fan-out + event dispatch (P0 of docs/specs/prebuilt-deploys.md).
#     GitHub sends EVERY webhook on a repo the same X-GitHub-Delivery GUID for
#     one event; each env's app has its own source and hook, so each must
#     deploy. A retry on the SAME source must still be deduped, and a ping
#     must answer pong.
hook() { # hook <source> <secret> <event> <guid> <body> → "<code> <body>"
  local sig
  sig="sha256=$(printf '%s' "$5" | openssl dgst -sha256 -hmac "$2" | awk '{print $NF}')"
  curl -s -w ' %{http_code}' -H "X-GitHub-Event: $3" -H "X-GitHub-Delivery: $4" \
    -H "X-Hub-Signature-256: $sig" -H "Content-Type: application/json" \
    -d "$5" "$BASE/hooks/$1"
}
curl -sf -b "$JAR" -o /dev/null -d "name=$SLUG2&csrf_token=$CSRF" "$BASE/projects/$PROJ_SLUG/apps" || fail "create second app"
seed_out2="$(env DEPLOYMATE_DATA_DIR="$DATA_DIR" "$BIN" seed-git-source "$SLUG2" "$BARE" main github)" \
  || fail "seed-git-source (second app)"
SOURCE_ID2="$(echo "$seed_out2" | grep '^source_id=' | cut -d= -f2)"
SECRET2="$(echo "$seed_out2" | grep '^webhook_secret=' | cut -d= -f2)"
[ -n "$SOURCE_ID2" ] && [ "$SOURCE_ID2" != "$SOURCE_ID" ] || fail "second app must get its own git source"
out="$(hook "$SOURCE_ID2" "$SECRET2" push "e2e-$STAMP" "$BODY")"
[ "$out" = "queued 200" ] || fail "second source with the SAME delivery GUID must queue, got: $out"
log "PASS: same X-GitHub-Delivery GUID deployed BOTH apps (per-source de-dupe)"
out="$(hook "$SOURCE_ID" "$SECRET" push "e2e-$STAMP" "$BODY")"
[ "$out" = "duplicate delivery ignored 200" ] || fail "retry on the same source must be deduped, got: $out"
out="$(hook "$SOURCE_ID" "$SECRET" ping "e2e-ping-$STAMP" '{"zen":"e2e"}')"
[ "$out" = "pong 200" ] || fail "ping must answer pong, got: $out"
out="$(hook "$SOURCE_ID" "$SECRET" workflow_run "e2e-wf-$STAMP" '{"action":"completed"}')"
[ "$out" = "ignored: not a push event 200" ] || fail "non-push events must be ignored, got: $out"
log "PASS: retry deduped, ping pongs, non-push events ignored"

# 6. Wait for the worker to build + start the container (Dockerfile build of
#    nginx:alpine — base image is local, so this is fast)
log "waiting for $CONTAINER to run"
for i in $(seq 1 90); do
  if [ "$(docker inspect "$CONTAINER" --format '{{.State.Running}}' 2>/dev/null)" = "true" ]; then
    log "$CONTAINER running"
    break
  fi
  kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server died"; }
  sleep 2
  [ "$i" = 90 ] && { tail -40 "$DATA_DIR/server.log"; fail "container never started"; }
done

log "waiting for $CONTAINER2 (second app) to run"
for i in $(seq 1 90); do
  [ "$(docker inspect "$CONTAINER2" --format '{{.State.Running}}' 2>/dev/null)" = "true" ] && break
  kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server died"; }
  sleep 2
  [ "$i" = 90 ] && { tail -40 "$DATA_DIR/server.log"; fail "second app's container never started"; }
done
log "$CONTAINER2 running"

# 7. HTTP-probe the running app through the preview proxy (session-protected)
log "probing $BASE/preview/$SLUG/"
for i in $(seq 1 15); do
  out="$(curl -s -b "$JAR" "$BASE/preview/$SLUG/" || true)"
  if echo "$out" | grep -q "deploymate e2e git fixture"; then
    log "PASS: app served expected content through the preview proxy"
    break
  fi
  sleep 1
  [ "$i" = 15 ] && fail "app did not serve expected content (last: $out)"
done
for i in $(seq 1 15); do
  out="$(curl -s -b "$JAR" "$BASE/preview/$SLUG2/" || true)"
  if echo "$out" | grep -q "deploymate e2e git fixture"; then
    log "PASS: second app (same GUID, own source) also serves the fixture"
    break
  fi
  sleep 1
  [ "$i" = 15 ] && fail "second app did not serve expected content (last: $out)"
done

# 8. The built image's on-disk size must be recorded (best-effort inspect
#    at build time) and surface on /stats as the tracked app-images total.
#    (Two apps now → the tracked total is the SUM of both images.)
SZ="$(sqlite3 "$DATA_DIR/data.db" "SELECT SUM(size_bytes) FROM images" 2>/dev/null || true)"
[ -n "$SZ" ] && [ "$SZ" -gt 0 ] 2>/dev/null || fail "images.size_bytes not recorded (got '$SZ')"
expected="$(awk -v n="$SZ" 'BEGIN{
  if (n < 1024) printf "%d B", n;
  else if (n < 1048576) printf "%.1f KB", n/1024;
  else if (n < 1073741824) printf "%.1f MB", n/1048576;
  else printf "%.1f GB", n/1073741824 }')"
stats="$(curl -s -b "$JAR" "$BASE/stats")"
echo "$stats" | grep -q "Storage" || fail "stats page missing the Storage panel"
echo "$stats" | grep -q "tracked app images" || fail "stats page missing tracked app images"
echo "$stats" | grep -qF "$expected" || fail "stats tracked total '$expected' missing (size $SZ)"
log "PASS: image size recorded ($SZ bytes) and shown on /stats ($expected)"
exit 0
