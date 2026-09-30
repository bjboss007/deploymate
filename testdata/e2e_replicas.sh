#!/usr/bin/env bash
# DeployMate app-replicas e2e (docs/specs/app-replicas.md), against a
# THROWAWAY server so it never touches the user's live :8090 apps.
#
#   deploy traefik/whoami (answers with its container hostname, so every
#   response names the replica that served it) with a routed domain
#   scale 1 → 2 (a `scale` deployment, no build)
#   Traefik label contract on both slots: distinct routers, ONE shared
#     service, identical service + healthcheck labels (Traefik itself cannot
#     run here — the LB semantics were spiked separately, see the spec)
#   /preview round-robins across both replicas
#   stop slot 2 → /preview keeps serving (skip/failover) → monitor heal
#     restarts it
#   merged logs carry [r1]/[r2] prefixes; ?replica=r2 narrows
#   rolling redeploy at N=2 with preview traffic running: zero non-200s,
#     both slots on the new deployment
#   scale 2 → 3 → 1 (+ `scale` rows), cap rejected, delete removes all
#
# Everything is cleaned up on exit. The slug is timestamped so container
# names can never match the user's real containers.
#
#   ./testdata/e2e_replicas.sh      # builds ./bin/deploymate first
#   DM_PORT=18096 ./testdata/e2e_replicas.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DM_BIN:-$ROOT/bin/deploymate}"
PORT="${DM_PORT:-18096}"
BASE="http://127.0.0.1:$PORT"
EMAIL="owner@e2e.dev"
PASSWORD="e2epassword123"
STAMP="$(date +%s)"
SLUG="e2erep$STAMP"
IMAGE="traefik/whoami"

DATA_DIR="$(mktemp -d)"
JAR="$(mktemp)"
SRV_PID=""
LOAD_PID=""

log()  { echo "== $*"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  set +e
  [ -n "$LOAD_PID" ] && kill "$LOAD_PID" 2>/dev/null
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null && wait "$SRV_PID" 2>/dev/null
  # Only this run's containers: the slug is timestamped, so the anchored
  # prefix cannot match anything else (slots, staged leftovers included).
  for c in $(docker ps -aq --filter "name=^dm-$SLUG" 2>/dev/null); do
    docker rm -f "$c" >/dev/null 2>&1
  done
  rm -rf "$DATA_DIR" "$JAR"
}
trap cleanup EXIT

if [ -z "${DM_BIN:-}" ]; then
  log "building binary"
  make -C "$ROOT" build >/dev/null
fi
[ -x "$BIN" ] || fail "binary not found at $BIN"
docker image inspect "$IMAGE" >/dev/null 2>&1 || docker pull -q "$IMAGE" >/dev/null

# No Cloudflare vars → auto-DNS stays off, no real DNS records created.
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

curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" || fail "login"
csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }
db() { sqlite3 "$DATA_DIR/data.db" "$1" 2>/dev/null || true; }

CSRF="$(csrf "$BASE/projects")"
PROJ="e2e-$STAMP"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
curl -sf -b "$JAR" -o /dev/null -d "name=$SLUG&csrf_token=$CSRF" "$BASE/projects/$PROJ/apps" || fail "create app"
C="$(csrf "$BASE/apps/$SLUG")"
curl -sf -b "$JAR" -o /dev/null -d "hostname=$SLUG.example.test&csrf_token=$C" "$BASE/apps/$SLUG/domains" || fail "add domain"
log "project + app + domain created"

# post_to <path> <form> → prints the /deployments/{id} id (fails otherwise)
post_to() {
  local out code loc
  out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' -d "$2&csrf_token=$(csrf "$BASE/apps/$SLUG")" "$BASE$1")"
  code="${out%% *}" loc="${out#* }"
  [ "$code" = "303" ] || fail "POST $1 returned $code (want 303): $out"
  case "$loc" in
    */deployments/*) echo "${loc#*deployments/}" ;;
    *) fail "POST $1 redirected to $loc, want /deployments/{id}" ;;
  esac
}
wait_status() { # wait_status <id> <want> <secs>
  local row=""
  for _ in $(seq 1 "$3"); do
    row="$(db "SELECT status || '|' || error FROM deployments WHERE id='$1'")"
    [ "${row%%|*}" = "$2" ] && return 0
    kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server died"; }
    sleep 1
  done
  echo "last: $row" >&2
  return 1
}
running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }
preview_hosts() { # preview_hosts <n> → distinct replica hostnames that answered
  for _ in $(seq 1 "$1"); do
    curl -sf -b "$JAR" "$BASE/preview/$SLUG/" | grep -m1 '^Hostname:' | awk '{print $2}'
  done | sort -u
}

# 1. Deploy (N=1) and scale to 2.
DID="$(post_to "/apps/$SLUG/deploy" "image=$IMAGE&port=80")"
wait_status "$DID" running 120 || fail "deploy $DID never reached running"
log "deployed $IMAGE ($DID)"

SID="$(post_to "/apps/$SLUG/replicas" "replicas=2")"
wait_status "$SID" running 90 || fail "scale $SID never reached running"
[ "$(db "SELECT kind FROM deployments WHERE id='$SID'")" = "scale" ] || fail "scale row kind"
running "dm-$SLUG" && running "dm-$SLUG-r2" || fail "both slot containers must run after scale 1→2"
[ "$(db "SELECT COUNT(*) FROM app_replicas r JOIN apps a ON a.id=r.app_id WHERE a.slug='$SLUG'")" = "2" ] || fail "want 2 replica rows"
[ "$(db "SELECT current_deployment_id FROM apps WHERE slug='$SLUG'")" = "$DID" ] || fail "scale must not change current_deployment_id"
log "scaled 1 → 2 ($SID): dm-$SLUG + dm-$SLUG-r2 running"

# 2. Traefik label contract (container inspect).
labels() { docker inspect -f '{{range $k, $v := .Config.Labels}}{{$k}}={{$v}}{{"\n"}}{{end}}' "$1" | grep '^traefik\.' | sort; }
L1="$(labels "dm-$SLUG")"; L2="$(labels "dm-$SLUG-r2")"
R1="$(echo "$L1" | grep -oE '^traefik\.http\.routers\.[^.]+' | sort -u)"
R2="$(echo "$L2" | grep -oE '^traefik\.http\.routers\.[^.]+' | sort -u)"
[ "$(echo "$R1" | wc -l | tr -d ' ')" = "1" ] && [ "$(echo "$R2" | wc -l | tr -d ' ')" = "1" ] || fail "each slot must own exactly one router"
[ "$R1" != "$R2" ] || fail "slots must NOT share a router name ($R1) — Traefik drops conflicting routers"
S1="$(echo "$L1" | grep '\.service=' | cut -d= -f2)"; S2="$(echo "$L2" | grep '\.service=' | cut -d= -f2)"
[ -n "$S1" ] && [ "$S1" = "$S2" ] || fail "routers must target one shared service ($S1 vs $S2)"
SV1="$(echo "$L1" | grep "^traefik\.http\.services\.$S1\.")"; SV2="$(echo "$L2" | grep "^traefik\.http\.services\.$S1\.")"
[ "$SV1" = "$SV2" ] || fail "service labels must be byte-identical across slots:\n$SV1\n--\n$SV2"
echo "$SV1" | grep -q "loadbalancer.healthcheck.path=/$" || fail "healthcheck path label missing"
log "labels: routers $(basename "$R1") / $(basename "$R2") → shared service $S1 (+ healthcheck)"

# 3. /preview round-robin across both replicas.
N_HOSTS="$(preview_hosts 8 | wc -l | tr -d ' ')"
[ "$N_HOSTS" = "2" ] || fail "preview must round-robin across 2 replicas, saw $N_HOSTS distinct"
log "/preview round-robins across both replicas"

# 4. Stop slot 2 → preview keeps serving → the monitor's heal restarts it.
docker stop -t 1 "dm-$SLUG-r2" >/dev/null
BAD=0
for _ in $(seq 1 8); do
  code="$(curl -s -o /dev/null -w '%{http_code}' -b "$JAR" "$BASE/preview/$SLUG/")"
  [ "$code" = "200" ] || BAD=$((BAD + 1))
done
[ "$BAD" = "0" ] || fail "$BAD/8 preview requests failed with slot 2 stopped (want failover)"
log "slot 2 stopped: 8/8 preview requests still 200 (failover)"
for i in $(seq 1 75); do
  running "dm-$SLUG-r2" && break
  sleep 1
  [ "$i" = 75 ] && fail "monitor never healed slot 2"
done
log "monitor healed slot 2 (container running again)"

# 5. Merged logs with prefixes, and the per-replica filter.
ALL="$(curl -s -N -b "$JAR" --max-time 3 "$BASE/apps/$SLUG/logs" || true)"
echo "$ALL" | grep -q '^data: \[r1\]' && echo "$ALL" | grep -q '^data: \[r2\]' || fail "merged logs must carry [r1] and [r2] prefixes"
ONE="$(curl -s -N -b "$JAR" --max-time 3 "$BASE/apps/$SLUG/logs?replica=r2" || true)"
echo "$ONE" | grep -q '^data: \[r2\]' && ! echo "$ONE" | grep -q '^data: \[r1\]' || fail "?replica=r2 must narrow to slot 2"
log "logs merged with [rN] prefixes; ?replica=r2 narrows"

# 6a. Health path: saved + checked on every replica right away.
FLASH="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "health_path=/health&csrf_token=$(csrf "$BASE/apps/$SLUG")" "$BASE/apps/$SLUG/health-path")"
echo "$FLASH" | grep -q "r1+200" && echo "$FLASH" | grep -q "r2+200" || fail "health path check must report r1/r2 200 (flash: $FLASH)"
[ "$(db "SELECT health_path FROM apps WHERE slug='$SLUG'")" = "/health" ] || fail "health path not saved"
BADP="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "health_path=//evil.test&csrf_token=$(csrf "$BASE/apps/$SLUG")" "$BASE/apps/$SLUG/health-path")"
[ "$(db "SELECT health_path FROM apps WHERE slug='$SLUG'")" = "/health" ] || fail "invalid health path must be rejected ($BADP)"
log "health path /health saved and checked on both replicas; //evil.test rejected"

# 6b. Rolling redeploy at N=2 under preview load: never a non-200.
( while :; do
    curl -s -o /dev/null -w '%{http_code}\n' -b "$JAR" --max-time 5 "$BASE/preview/$SLUG/"
    sleep 0.1
  done >"$DATA_DIR/load.txt" ) &
LOAD_PID=$!
D2="$(post_to "/apps/$SLUG/deploy" "image=$IMAGE&port=80")"
wait_status "$D2" running 120 || fail "rolling deploy $D2 never reached running"
sleep 1
kill "$LOAD_PID" 2>/dev/null; wait "$LOAD_PID" 2>/dev/null || true; LOAD_PID=""
TOTAL="$(wc -l <"$DATA_DIR/load.txt" | tr -d ' ')"
NON200="$(grep -vc '^200$' "$DATA_DIR/load.txt" || true)"
[ "$NON200" = "0" ] || fail "$NON200/$TOTAL preview requests failed during the rolling deploy"
ON_NEW="$(db "SELECT COUNT(*) FROM app_replicas r JOIN apps a ON a.id=r.app_id WHERE a.slug='$SLUG' AND r.deploy_id='$D2'")"
[ "$ON_NEW" = "2" ] || fail "both slots must record the new deployment (got $ON_NEW)"
grep -q "replica 2/2" <<<"$(db "SELECT line FROM build_logs WHERE deployment_id='$D2'")" || fail "build log must show the per-replica rollout"
log "rolling deploy $D2: $TOTAL preview requests during rollout, 0 failed; both slots on it"
# The new health path reaches Traefik on this deploy: a new config-hash
# service, shared by both slots, with healthcheck.path=/health.
L1="$(labels "dm-$SLUG")"; L2="$(labels "dm-$SLUG-r2")"
NS1="$(echo "$L1" | grep '\.service=' | cut -d= -f2)"; NS2="$(echo "$L2" | grep '\.service=' | cut -d= -f2)"
[ "$NS1" = "$NS2" ] && [ "$NS1" != "$S1" ] || fail "redeploy must move both slots to one new service (old $S1, got $NS1 / $NS2)"
echo "$L1" | grep -q "^traefik\.http\.services\.$NS1\.loadbalancer\.healthcheck\.path=/health$" || fail "healthcheck label must carry /health"
log "redeploy moved both slots to service $NS1 with healthcheck.path=/health"

# 7. Scale 2 → 3 → 1, cap rejected.
S3="$(post_to "/apps/$SLUG/replicas" "replicas=3")"
wait_status "$S3" running 90 || fail "scale to 3 failed"
running "dm-$SLUG-r3" || fail "slot 3 must run after scale 2→3"
[ "$(preview_hosts 9 | wc -l | tr -d ' ')" = "3" ] || fail "preview must reach 3 replicas"
S1D="$(post_to "/apps/$SLUG/replicas" "replicas=1")"
wait_status "$S1D" running 60 || fail "scale to 1 failed"
docker inspect "dm-$SLUG-r2" >/dev/null 2>&1 && fail "slot 2 container must be removed on scale-down"
docker inspect "dm-$SLUG-r3" >/dev/null 2>&1 && fail "slot 3 container must be removed on scale-down"
running "dm-$SLUG" || fail "slot 1 must survive scale-down"
[ "$(db "SELECT COUNT(*) FROM app_replicas r JOIN apps a ON a.id=r.app_id WHERE a.slug='$SLUG'")" = "1" ] || fail "want 1 replica row after scale-down"
[ "$(db "SELECT COUNT(*) FROM deployments d JOIN apps a ON a.id=d.app_id WHERE a.slug='$SLUG' AND d.kind='scale'")" = "3" ] || fail "want 3 scale deployment rows"
log "scaled 2 → 3 → 1 (3 scale rows; r2/r3 removed; slot 1 untouched)"

OUT="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' -d "replicas=6&csrf_token=$(csrf "$BASE/apps/$SLUG")" "$BASE/apps/$SLUG/replicas")"
echo "$OUT" | grep -q "flash=" || fail "replicas=6 must be rejected with a flash"
[ "$(db "SELECT replicas FROM apps WHERE slug='$SLUG'")" = "1" ] || fail "rejected count must not change replicas"
log "cap: replicas=6 rejected"

# 8. Delete removes every replica.
post_to_plain() { curl -s -b "$JAR" -o /dev/null -w '%{http_code}' -d "csrf_token=$(csrf "$BASE/apps/$SLUG")" "$BASE$1"; }
S2B="$(post_to "/apps/$SLUG/replicas" "replicas=2")"
wait_status "$S2B" running 90 || fail "scale back to 2 failed"
[ "$(post_to_plain "/apps/$SLUG/delete")" = "303" ] || fail "delete"
[ -z "$(docker ps -aq --filter "name=^dm-$SLUG")" ] || fail "delete must remove every replica container"
log "delete removed all replica containers"

echo "PASS: replicas e2e"
