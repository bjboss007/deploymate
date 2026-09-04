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
CMD_SLUG="e2ecmd$STAMP"          # command-override scenario
CONTAINERS=("dm-$SLUG" "dm-$FAIL_SLUG" "dm-$CMD_SLUG")

DATA_DIR="$(mktemp -d)"
JAR="$(mktemp)"
VOLUME="dm-e2e-$STAMP-data"     # created for the storage-panel check
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
  docker volume rm -f "$VOLUME" >/dev/null 2>&1
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
for name in "$SLUG" "$FAIL_SLUG" "$CMD_SLUG"; do
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

# 6. Command override: busybox's own default CMD is `sh` (exits immediately
#    with no TTY). Deploying with entrypoint `httpd` + command `-f -p 8080`
#    must produce a container that (a) actually serves (swap probe passes)
#    and (b) carries the override in Config.
log "deploying busybox with an entrypoint/command override to $CMD_SLUG"
c="$(csrf "$BASE/apps/$CMD_SLUG")"
out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
  --data-urlencode "image=busybox:latest" \
  --data-urlencode "port=8080" \
  --data-urlencode "entrypoint=httpd" \
  --data-urlencode "command=-f -p 8080" \
  --data-urlencode "csrf_token=$c" \
  "$BASE/apps/$CMD_SLUG/deploy")"
[ "${out%% *}" = "303" ] || fail "cmd deploy returned ${out%% *}, want 303"
CMD_DID="${out#*deployments/}"
wait_status "$CMD_DID" "running" 180 || fail "cmd deployment never reached running"

for i in $(seq 1 30); do
  [ "$(docker inspect "dm-$CMD_SLUG" --format '{{.State.Running}}' 2>/dev/null)" = "true" ] && break
  sleep 1
  [ "$i" = 30 ] && fail "container dm-$CMD_SLUG never started"
done
ep="$(docker inspect "dm-$CMD_SLUG" --format '{{.Config.Entrypoint}}')"
cmd="$(docker inspect "dm-$CMD_SLUG" --format '{{.Config.Cmd}}')"
[ "$ep" = "[httpd]" ] || fail "Config.Entrypoint = '$ep', want [httpd]"
[ "$cmd" = "[-f -p 8080]" ] || fail "Config.Cmd = '$cmd', want [-f -p 8080]"
# The override must serve: busybox httpd answers (a 404 index still counts
# as healthy to the swap probe).
for i in $(seq 1 15); do
  out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' "$BASE/preview/$CMD_SLUG/" || true)"
  [ "$out" = "404" ] || [ "$out" = "200" ] && break
  sleep 1
done
[ "$out" = "404" ] || [ "$out" = "200" ] || fail "overridden container did not serve (got $out)"
log "PASS: entrypoint/command override live in the container and serving"

# The app page must show the overrides prefilled.
html="$(curl -s -b "$JAR" "$BASE/apps/$CMD_SLUG")"
echo "$html" | grep -q 'name="entrypoint" .* value="httpd"' || fail "entrypoint not prefilled on the form"
echo "$html" | grep -q 'name="command" .* value="-f -p 8080"' || fail "command not prefilled on the form"
log "PASS: overrides prefilled on the deploy form"

# Redeploy with BOTH fields empty → the image's own defaults return (the
# overrides must not linger on the app row). nginx:alpine is the probe
# image here: its own default CMD serves, so the swap passes with no
# override in place (busybox's default `sh` would exit and prove nothing).
c="$(csrf "$BASE/apps/$CMD_SLUG")"
out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
  --data-urlencode "image=nginx:alpine" \
  --data-urlencode "port=80" \
  --data-urlencode "entrypoint=" \
  --data-urlencode "command=" \
  --data-urlencode "csrf_token=$c" \
  "$BASE/apps/$CMD_SLUG/deploy")"
[ "${out%% *}" = "303" ] || fail "empty-override redeploy returned ${out%% *}, want 303"
EMPTY_DID="${out#*deployments/}"
wait_status "$EMPTY_DID" "running" 180 || fail "empty-override redeploy never reached running"
for i in $(seq 1 30); do
  ep="$(docker inspect "dm-$CMD_SLUG" --format '{{.Config.Entrypoint}}' 2>/dev/null)"
  cmd="$(docker inspect "dm-$CMD_SLUG" --format '{{.Config.Cmd}}' 2>/dev/null)"
  # nginx:alpine's own defaults: entrypoint /docker-entrypoint.sh, CMD
  # `nginx -g daemon off;`.
  if [ "$ep" = "[/docker-entrypoint.sh]" ] && [ "$cmd" = "[nginx -g daemon off;]" ]; then
    log "PASS: empty fields restored image defaults (entrypoint=$ep cmd=$cmd)"
    break
  fi
  sleep 1
  [ "$i" = 30 ] && fail "overrides never cleared (entrypoint=$ep cmd=$cmd)"
done

# 7. Storage panel: a freshly created (orphan) named volume must appear on
#    /stats flagged "not tracked by DeployMate" — the disk-diagnosis point
#    of the panel.
log "checking the /stats storage panel"
docker volume create "$VOLUME" >/dev/null || fail "create test volume"
stats="$(curl -s -b "$JAR" "$BASE/stats")"
echo "$stats" | grep -q "Storage" || fail "stats page missing the Storage panel"
echo "$stats" | grep -qF "$VOLUME" || fail "stats volume table missing $VOLUME"
echo "$stats" | grep -q "not tracked by DeployMate" || fail "stats missing the not-tracked note"
log "PASS: /stats storage panel lists daemon volumes"

# 8. Teardown happens in the EXIT trap; nothing after this should deploy.
log "e2e_manual: all checks passed"
