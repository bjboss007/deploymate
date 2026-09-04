#!/usr/bin/env bash
# DeployMate auto-DNS lifecycle e2e: app create → preview CNAME appears in
# the REAL Cloudflare zone; app delete → the CNAME is gone. Runs against a
# THROWAWAY server (spare port + scratch data dir + fresh owner) so the
# user's live :8090 apps are untouched, but with the real Cloudflare vars
# so the record flows through the actual API. The test deletes the record
# itself — that IS the assertion — so nothing lingers in the zone even on
# success; the trap cleans up on failure too.
#
# Requires the Cloudflare env vars (DEPLOYMATE_CLOUDFLARE_*; the shell's
# CLOUD_FLARE_TOKEN/CLOUD_FLARE_ZONE_ID aliases are accepted as fallback).
# Without them auto-DNS is off by design — fail fast rather than silently
# test nothing.
#
#   ./testdata/e2e_dns.sh        # builds ./bin/deploymate first if absent
#   DM_PORT=18093 ./testdata/e2e_dns.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${DM_BIN:-$ROOT/bin/deploymate}"
PORT="${DM_PORT:-18093}"
BASE="http://127.0.0.1:$PORT"
EMAIL="owner@e2e.dev"
PASSWORD="e2epassword123"
STAMP="$(date +%s)"
PROJ_SLUG="dnse2e$STAMP"
SLUG="dnsdel$STAMP"              # timestamped: cannot collide with a real app
HOST="$SLUG.dm.getmerchanttech.com"

DATA_DIR="$(mktemp -d)"
JAR="$(mktemp)"
SRV_PID=""

# Cloudflare creds: DEPLOYMATE_* wins, CLOUD_FLARE_* aliases fall back
# (the shell aliases from dev-environment.md).
CF_TOKEN="${DEPLOYMATE_CLOUDFLARE_API_TOKEN:-${CLOUD_FLARE_TOKEN:-}}"
CF_ZONE="${DEPLOYMATE_CLOUDFLARE_ZONE_ID:-${CLOUD_FLARE_ZONE_ID:-}}"
CF_TUNNEL="${DEPLOYMATE_CLOUDFLARE_TUNNEL_ID:-}"
PREVIEW_HOST="${DEPLOYMATE_PREVIEW_HOST:-dm.getmerchanttech.com}"
CF_API="https://api.cloudflare.com/client/v4"

log()  { echo "== $*"; }
fail() { echo "FAIL: $1" >&2; exit 1; }

cleanup() {
  set +e
  # If the run failed before the delete assertion, remove any record the
  # create step made so the real zone is left exactly as found.
  if [ -n "${RECORD_ID:-}" ]; then
    curl -sf -X DELETE "$CF_API/zones/$CF_ZONE/dns_records/$RECORD_ID" \
      -H "Authorization: Bearer $CF_TOKEN" >/dev/null 2>&1
    log "trap: removed leftover record $RECORD_ID"
  fi
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null && wait "$SRV_PID" 2>/dev/null
  rm -rf "$DATA_DIR" "$JAR"
}
trap cleanup EXIT

cf_records() { # -> number of records named $HOST in the zone
  curl -sf "$CF_API/zones/$CF_ZONE/dns_records?name=$HOST" \
    -H "Authorization: Bearer $CF_TOKEN" \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d["result"]))'
}

# 0. Prereqs + binary.
[ -n "$CF_TOKEN" ] && [ -n "$CF_ZONE" ] && [ -n "$CF_TUNNEL" ] \
  || fail "auto-DNS e2e needs the Cloudflare vars (DEPLOYMATE_CLOUDFLARE_* or CLOUD_FLARE_* aliases)"
[ "$(cf_records)" = "0" ] || fail "zone already has a record named $HOST — pick a fresh slug"
if [ -z "${DM_BIN:-}" ]; then
  log "building binary"
  make -C "$ROOT" build >/dev/null
fi
[ -x "$BIN" ] || fail "binary not found at $BIN"

# 1. Start a throwaway server with auto-DNS ON (fresh owner via setup env).
log "starting throwaway server on $PORT (data $DATA_DIR)"
env -i PATH="$PATH" HOME="$HOME" \
  DEPLOYMATE_ADDR="127.0.0.1:$PORT" \
  DEPLOYMATE_DATA_DIR="$DATA_DIR" \
  DEPLOYMATE_PREVIEW_HOST="$PREVIEW_HOST" \
  DEPLOYMATE_CLOUDFLARE_API_TOKEN="$CF_TOKEN" \
  DEPLOYMATE_CLOUDFLARE_ZONE_ID="$CF_ZONE" \
  DEPLOYMATE_CLOUDFLARE_TUNNEL_ID="$CF_TUNNEL" \
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

# 2. Log in and create a project + app (auto-DNS should create the CNAME).
curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" || fail "login"
csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }
CSRF="$(csrf "$BASE/projects")"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ_SLUG&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
LOC="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' \
  -d "name=$SLUG&csrf_token=$CSRF" "$BASE/projects/$PROJ_SLUG/apps")" || fail "create app"
echo "$LOC" | grep -q "/apps/$SLUG$" || fail "create app: location $LOC, want /apps/$SLUG"

# 3. The CNAME must exist in the real zone, proxied, pointing at the tunnel.
for i in $(seq 1 20); do
  [ "$(cf_records)" = "1" ] && break
  sleep 0.5
  [ "$i" = 20 ] && fail "record for $HOST never appeared in the zone"
done
RECORD_JSON="$(curl -sf "$CF_API/zones/$CF_ZONE/dns_records?name=$HOST" \
  -H "Authorization: Bearer $CF_TOKEN")"
RECORD_ID="$(echo "$RECORD_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"][0]["id"])')"
echo "$RECORD_JSON" | python3 -c '
import json,sys
r = json.load(sys.stdin)["result"][0]
assert r["name"] == "'$HOST'", r
assert r["type"] == "CNAME", r
assert r["content"] == "'$CF_TUNNEL'.cfargotunnel.com", r
assert r["proxied"] is True, r
print("record ok:", r["id"], r["name"], "->", r["content"])' || fail "created record wrong"
log "create path verified: CNAME live in the zone"

# 4. Delete the app — must 303 to the project page with NO warning flash
#    (a DNS failure would add ?flash=) and remove the CNAME.
C="$(csrf "$BASE/apps/$SLUG")"
LOC="$(curl -s -b "$JAR" -o /dev/null -w '%{redirect_url}' \
  -d "csrf_token=$C" "$BASE/apps/$SLUG/delete")" || fail "delete app"
echo "$LOC" | grep -q "^$BASE/projects/$PROJ_SLUG$" \
  || fail "delete app: location '$LOC', want $BASE/projects/$PROJ_SLUG (no flash)"
for i in $(seq 1 20); do
  [ "$(cf_records)" = "0" ] && break
  sleep 0.5
  [ "$i" = 20 ] && fail "record for $HOST still in the zone after app delete"
done
RECORD_ID=""
log "delete path verified: CNAME removed from the zone"

log "ALL DNS E2E CHECKS PASSED"
