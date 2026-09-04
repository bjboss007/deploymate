#!/usr/bin/env bash
# DeployMate database-backups e2e: opt-in → snapshot → break the data →
# restore → retention prune → stopped-skip, against a THROWAWAY server with
# a LOCAL backup destination (docs/specs/database-backups.md verification
# list). The local destination means the run never touches R2 or the live
# :8090 server; the Docker daemon is shared, so the service slug is
# timestamped for a unique container/volume.
#
#   spare port + scratch data dir + scratch dest dir
#   provision a real Postgres service (template path) + seed rows
#   enable via the config form   → key generated, row enabled
#   bad destination id           → rejected at save
#   "Back up now"                → .dump.enc + .meta.json in the dest
#   drop the table, restore      → rows are back
#   keep=1 + second backup       → older objects pruned
#   stop the service, back up    → backup_skipped event, no new object
#
#   ./testdata/e2e_backup.sh        # builds ./bin/deploymate first if absent
#   DM_PORT=18094 ./testdata/e2e_backup.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DM_BIN:-$ROOT/bin/deploymate}"
PORT="${DM_PORT:-18094}"
BASE="http://127.0.0.1:$PORT"
EMAIL="owner@e2e.dev"
PASSWORD="e2epassword123"
STAMP="$(date +%s)"
SLUG="e2edb$STAMP"            # timestamped slug: unique container + volume
CONTAINER="dm-svc-$SLUG"
VOLUME="dm-svc-$SLUG-data"

DATA_DIR="$(mktemp -d)"
DEST_DIR="$(mktemp -d)"       # the local backup destination (its own dir)
JAR="$(mktemp)"
SRV_PID=""

log()  { echo "== $*"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  set +e
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null && wait "$SRV_PID" 2>/dev/null
  docker rm -f "$CONTAINER" >/dev/null 2>&1
  docker volume rm -f "$VOLUME" >/dev/null 2>&1
  rm -rf "$DATA_DIR" "$DEST_DIR" "$JAR"
}
trap cleanup EXIT

# 0. Binary — build fresh so the suite always tests current code (unless the
#    caller pins a prebuilt one via DM_BIN).
if [ -z "${DM_BIN:-}" ]; then
  log "building binary"
  make -C "$ROOT" build >/dev/null
fi
[ -x "$BIN" ] || fail "binary not found at $BIN"

# 1. Start a throwaway server with one LOCAL backup destination ("default").
log "starting throwaway server on $PORT (data $DATA_DIR, dest $DEST_DIR)"
env -i PATH="$PATH" HOME="$HOME" \
  DEPLOYMATE_ADDR="127.0.0.1:$PORT" \
  DEPLOYMATE_DATA_DIR="$DATA_DIR" \
  DEPLOYMATE_SETUP_EMAIL="$EMAIL" \
  DEPLOYMATE_SETUP_PASSWORD="$PASSWORD" \
  DEPLOYMATE_BACKUP_DEST_DEFAULT_TYPE=local \
  DEPLOYMATE_BACKUP_DEST_DEFAULT_DIR="$DEST_DIR" \
  "$BIN" serve >"$DATA_DIR/server.log" 2>&1 &
SRV_PID=$!

for i in $(seq 1 50); do
  curl -sf -o /dev/null "$BASE/login" && break
  kill -0 "$SRV_PID" 2>/dev/null || { cat "$DATA_DIR/server.log"; fail "server exited during startup"; }
  sleep 0.2
  [ "$i" = 50 ] && { cat "$DATA_DIR/server.log"; fail "server never became ready"; }
done
log "server ready"

# 2. Login + create a project and a Postgres service; start it.
curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" || fail "login"
csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }

CSRF="$(csrf "$BASE/projects")"
PROJ="e2e-$STAMP"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
PROJ_SLUG="$(echo "$PROJ" | tr '[:upper:]' '[:lower:]')"
SVC_CSRF="$(csrf "$BASE/projects/$PROJ_SLUG")"
out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
  -d "name=$SLUG&type=postgres&csrf_token=$SVC_CSRF" "$BASE/projects/$PROJ_SLUG/services")"
[ "${out%% *}" = "303" ] || fail "create service: got $out"
log "service created ($SLUG)"

curl -sf -b "$JAR" -o /dev/null -d "csrf_token=$(csrf "$BASE/services/$SLUG")" "$BASE/services/$SLUG/start" || fail "start service"
# The start redirect only fires after the provisioner's own readiness probe,
# but the postgres entrypoint restarts the server once between its init
# phase and the foreground exec — poll until psql actually answers so the
# seed never lands in that gap.
for i in $(seq 1 90); do
  if docker exec "$CONTAINER" psql -U dm -d app -tAc "SELECT 1" >/dev/null 2>&1; then
    break
  fi
  if ! docker inspect -f '{{.State.Running}}' "$CONTAINER" >/dev/null 2>&1; then
    { cat "$DATA_DIR/server.log"; fail "service container gone during startup"; }
  fi
  [ "$i" = 90 ] && { cat "$DATA_DIR/server.log"; fail "postgres never accepted connections"; }
  sleep 1
done
log "service container accepting connections"

# db() runs a sqlite3 read against the server's live DB (single-conn writes
# may lock briefly; retry).
db() { sqlite3 "$DATA_DIR/data.db" "$1" 2>/dev/null || true; }
wait_kind() { # wait_kind <kind> <seconds> — poll the events table
  local kind="$1" secs="$2"
  for i in $(seq 1 "$((secs * 2))"); do
    [ "$(db "SELECT COUNT(*) FROM events WHERE kind='$kind'")" -ge 1 ] 2>/dev/null && return 0
    sleep 0.5
  done
  return 1
}
blob_count() { ls "$DEST_DIR/backups/$SLUG/" 2>/dev/null | grep -c '\.dump\.enc$' || true; }

# 3. Seed rows to snapshot.
docker exec "$CONTAINER" psql -U dm -d app -q -c "CREATE TABLE e2e (id int PRIMARY KEY)" || fail "seed create"
docker exec "$CONTAINER" psql -U dm -d app -q -c "INSERT INTO e2e VALUES (1),(2),(3)" || fail "seed insert"
log "seeded 3 rows"

# 4. Opt in through the real form.
out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
  -d "enabled=on&schedule=0 2 * * *&keep=14&destination=default&csrf_token=$(csrf "$BASE/services/$SLUG")" \
  "$BASE/services/$SLUG/backup")"
[ "${out%% *}" = "303" ] || fail "enable: got $out"
[ "$(db "SELECT enabled FROM service_backups")" = "1" ] || fail "service_backups.enabled != 1"
KEY_ENC="$(db "SELECT key_enc FROM service_backups")"
[ -n "$KEY_ENC" ] || fail "no backup key generated at opt-in"
log "backups enabled, key generated"

# 5. Bad destination id → rejected at save, row untouched.
out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
  -d "enabled=on&schedule=0 2 * * *&keep=14&destination=nope&csrf_token=$(csrf "$BASE/services/$SLUG")" \
  "$BASE/services/$SLUG/backup")"
case "$out" in
  303*unknown*) ;; # flash travels in the Location query
  *) fail "bad destination not rejected: got $out" ;;
esac
[ "$(db "SELECT destination FROM service_backups")" = "default" ] || fail "row destination changed"

# 6. Key download (the owner's off-box copy).
KEY="$(curl -s -b "$JAR" "$BASE/services/$SLUG/backup/key" | tr -d '\n')"
[ "${#KEY}" = "64" ] || fail "key download = ${#KEY} chars, want 64 hex"
log "backup key downloaded"

# 7. "Back up now" → object + meta land in the local destination.
curl -sf -b "$JAR" -o /dev/null -d "csrf_token=$(csrf "$BASE/services/$SLUG")" "$BASE/services/$SLUG/backup/now" || fail "backup now"
for i in $(seq 1 60); do
  [ "$(blob_count)" -ge 1 ] && break
  [ "$i" = 60 ] && fail "no backup object appeared in $DEST_DIR"
  sleep 0.5
done
BLOB="$(ls "$DEST_DIR/backups/$SLUG/" | grep '\.dump\.enc$' | head -1)"
META="$(ls "$DEST_DIR/backups/$SLUG/" | grep '\.meta\.json$' | head -1)"
[ -s "$DEST_DIR/backups/$SLUG/$BLOB" ] || fail "backup object is empty"
META_SHA="$(grep -oE '"sha256":"[a-f0-9]+"' "$DEST_DIR/backups/$SLUG/$META" | cut -d'"' -f4)"
ACTUAL_SHA="$(shasum -a 256 "$DEST_DIR/backups/$SLUG/$BLOB" | cut -d' ' -f1)"
[ -n "$META_SHA" ] && [ "$META_SHA" = "$ACTUAL_SHA" ] || fail "meta sha $META_SHA != blob sha $ACTUAL_SHA"
wait_kind backup_ok 30 || fail "no backup_ok event"
log "backup object + meta verified (sha matches)"

# 8. Break the data, restore from the object, rows back.
docker exec "$CONTAINER" psql -U dm -d app -q -c "DROP TABLE e2e" || fail "drop table"
[ "$(docker exec "$CONTAINER" psql -U dm -d app -tAc "SELECT COUNT(*) FROM pg_tables WHERE tablename='e2e'")" = "0" ] || fail "table still there"
OBJ_KEY="$(curl -s -b "$JAR" "$BASE/services/$SLUG" | grep -oE 'name="object_key" value="[^"]+' | head -1 | cut -d'"' -f4)"
[ -n "$OBJ_KEY" ] || fail "no object_key on the service page"
out="$(curl -s -b "$JAR" -o /dev/null -w '%{http_code} %{redirect_url}' \
  -d "object_key=$OBJ_KEY&confirm=$SLUG&csrf_token=$(csrf "$BASE/services/$SLUG")" \
  "$BASE/services/$SLUG/backups/restore")"
[ "${out%% *}" = "303" ] || fail "restore POST: got $out"
if ! wait_kind restore_ok 90; then
  echo "--- recent events:"; db "SELECT id, kind, data FROM events ORDER BY id DESC LIMIT 6"
  echo "--- container logs:"; docker logs --tail 30 "$CONTAINER" 2>&1
  fail "no restore_ok event"
fi
for i in $(seq 1 30); do
  n="$(docker exec "$CONTAINER" psql -U dm -d app -tAc "SELECT COUNT(*) FROM e2e" 2>/dev/null || true)"
  [ "$n" = "3" ] && break
  [ "$i" = 30 ] && fail "restored row count = $n, want 3"
  sleep 1
done
log "restore brought the 3 rows back"

# 9. Retention: keep=1, second backup prunes the first object.
curl -sf -b "$JAR" -o /dev/null -d "enabled=on&schedule=0 2 * * *&keep=1&destination=default&csrf_token=$(csrf "$BASE/services/$SLUG")" \
  "$BASE/services/$SLUG/backup" || fail "set keep=1"
curl -sf -b "$JAR" -o /dev/null -d "csrf_token=$(csrf "$BASE/services/$SLUG")" "$BASE/services/$SLUG/backup/now" || fail "backup now (retention)"
for i in $(seq 1 60); do
  [ "$(blob_count)" = "1" ] && break
  [ "$i" = 60 ] && fail "after keep=1, blob count = $(blob_count), want 1"
  sleep 0.5
done
log "retention pruned to the newest object"

# 10. Stopped service → skip event, no new object.
curl -sf -b "$JAR" -o /dev/null -d "csrf_token=$(csrf "$BASE/services/$SLUG")" "$BASE/services/$SLUG/stop" || fail "stop service"
for i in $(seq 1 30); do
  state="$(docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null || true)"
  [ "$state" = "false" ] && break
  sleep 1
done
curl -sf -b "$JAR" -o /dev/null -d "csrf_token=$(csrf "$BASE/services/$SLUG")" "$BASE/services/$SLUG/backup/now" || fail "backup now (stopped)"
wait_kind backup_skipped 30 || fail "no backup_skipped event"
sleep 1.5 # give a misbehaving run a moment before the count
[ "$(blob_count)" = "1" ] || fail "stopped-service run added an object: $(blob_count)"
log "stopped service skipped cleanly"

log "ALL PASS — backup e2e complete"
