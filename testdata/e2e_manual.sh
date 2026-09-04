#!/usr/bin/env bash
# DeployMate manual-deploy e2e: the dashboard "Deploy" button path, against a
# THROWAWAY server so it never touches the user's live :8090 apps.
#
#   spare port + scratch data dir  →  fresh owner + project + app
#   POST /apps/{slug}/deploy       →  must return 303 to /deployments/{id}
#                                     (async: the request no longer blocks on
#                                     the pull — the WORKER does the work)
#   poll sqlite3 for status=running → container running → /preview serves
#   failure path: a bogus image fails with a pull error in the DB
#
# Everything is cleaned up on exit (server, containers, temp dirs). The
# Docker daemon is shared with the user's real server, so the app slug is
# timestamped to guarantee a unique container name / preview port.
#
#   ./testdata/e2e_manual.sh        # builds ./bin/deploymate first if absent
#   DM_PORT=18092 ./testdata/e2e_manual.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DM_BIN:-$ROOT/bin/deploymate}"
PORT="${DM_PORT:-18092}"
BASE="http://127.0.0.1:$PORT"
EMAIL="owner@e2e.dev"
PASSWORD="e2epassword123"
STAMP="$(date +%s)"
SLUG="e2eman$STAMP"              # timestamped slug: unique container/preview port
FAIL_SLUG="e2efail$STAMP"
CONTAINERS=("dm-$SLUG" "dm-$FAIL_SLUG")

DATA_DIR="$(mktemp -d)"
JAR="$(mktemp)"
SRV_PID=""

log()  { echo "== $*"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null && wait "$SRV_PID" 2>/dev/null
  # Remove only THIS run's containers: the canonical names plus any staged
  # leftovers (dm-{slug}-{32-hex}); the slug is timestamped, so the prefix
  # match cannot touch the user's containers.
  for name in "${CONTAINERS[@]}"; do
    docker rm -f "$name" >/dev/null 2>&1
    for staged in $(docker ps -aq --filter "name=^$name-[0-9a-f]\{32\}$" 2>/dev/null); do
      docker rm -f "$staged" >/dev/null 2>&1
    done
  done
  rm -rf "$DATA_DIR" "$JAR"
}
trap cleanup EXIT

# 0. Binary — build fresh so the suite always tests current code (unless the
#    caller pins a prebuilt one via DM_BIN).
if [ -z "${DM_BIN:-}" ]; then
  log "building binary"
  make -C "$ROOT" build >/dev/null
fi
[ -x "$BIN" ] || fail "binary not found at $BIN"

# 1. Start a throwaway server (fresh owner via setup env). Deliberately NO
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

# 2. Login + create project and apps
curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" || fail "login"
csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }

CSRF="$(csrf "$BASE/projects")"
PROJ="e2e-$STAMP"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
PROJ_SLUG="$(echo "$PROJ" | tr '[:upper:]' '[:lower:]')"
for name in "$SLUG" "$FAIL_SLUG"; do
  curl -sf -b "$JAR" -o /dev/null -d "name=$name&csrf_token=$CSRF" "$BASE/projects/$PROJ_SLUG/apps" || fail "create app $name"
done
log "project + apps created"

# db() runs a sqlite3 read against the server's live DB (single-conn writes
# may lock briefly; retry).
db() { sqlite3 "$DATA_DIR/data.db" "$1" 2>/dev/null || true; }

# deploy() posts the image form and returns the deployment id from the
# 303 Location — asserting the request itself was FAST (async queueing).
deploy() {
  local slug="$1" image="$2"
  local c
  c="$(csrf "$BASE/apps/$slug")"
  local out
  out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
    -d "image=$image&port=80&csrf_token=$c" "$BASE/apps/$slug/deploy")"
  local code="${out%% *}" loc="${out#* }"
  [ "$code" = "303" ] || fail "deploy returned $code, want 303 (got: $out)"
  # curl's %{redirect_url} resolves the Location against the request URL, so
  # it may be absolute; pull out the /deployments/{id} path either way.
  case "$loc" in
    /deployments/*) echo "${loc#/deployments/}" ;;
    */deployments/*) echo "${loc#*deployments/}" ;;
    *) fail "deploy redirected to $loc, want /deployments/{id}" ;;
  esac
}

wait_status() { # wait_status <id> <want-status> <timeout-secs> [err-contains]
  local id="$1" want="$2" secs="$3" want_err="${4:-}" row=""
  for i in $(seq 1 "$secs"); do
    row="$(db "SELECT status || '|' || error FROM deployments WHERE id='$id'")"
    local status="${row%%|*}"
    if [ "$status" = "$want" ]; then
      if [ -n "$want_err" ]; then
        echo "$row" | grep -q "$want_err" || continue
      fi
      return 0
    fi
    kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server died"; }
    sleep 1
  done
  echo "last: $row" >&2
  return 1
}

# 3. Happy path: deploy nginx — must be queued, not executed inline.
log "deploying nginx:alpine to $SLUG"
DID="$(deploy "$SLUG" "nginx:alpine")"
log "deployment $DID queued (request returned immediately)"
wait_status "$DID" "running" 120 || fail "deployment $DID never reached running"
db "SELECT 'status=' || status FROM deployments WHERE id='$DID'" >/dev/null
log "deployment $DID running"

# The worker must have set the app running/healthy + current deployment.
db_check="$(db "SELECT status FROM apps WHERE slug='$SLUG'")"
[ "$db_check" = "running" ] || fail "app status = '$db_check', want running"

# 4. Container must be up and serving through the preview proxy.
for i in $(seq 1 30); do
  if [ "$(docker inspect "dm-$SLUG" --format '{{.State.Running}}' 2>/dev/null)" = "true" ]; then
    log "dm-$SLUG running"
    break
  fi
  sleep 1
  [ "$i" = 30 ] && fail "container dm-$SLUG never started"
done
for i in $(seq 1 15); do
  out="$(curl -s -b "$JAR" "$BASE/preview/$SLUG/" || true)"
  if echo "$out" | grep -qi "welcome to nginx"; then
    log "PASS: nginx served through the preview proxy"
    break
  fi
  sleep 1
  [ "$i" = 15 ] && fail "app did not serve nginx (last: $out)"
done

# 5. Failure path: a bogus image fails fast with a pull error (the 10-minute
#    pull timeout itself is exercised only by the worker unit test — a real
#    hung registry would make this suite slow).
log "deploying a bogus image to $FAIL_SLUG"
DID2="$(deploy "$FAIL_SLUG" "no-such-registry.invalid/nope:latest")"
wait_status "$DID2" "failed" 90 "pull" || fail "bogus image did not fail with a pull error"
log "PASS: bogus image failed with a pull error"

# 6. Teardown happens in the EXIT trap; nothing after this should deploy.
log "e2e_manual: all checks passed"
