#!/usr/bin/env bash
# DeployMate API smoke test: login → project → app → deploy nginx → verify.
# Run against a started server:
#   DEPLOYMATE_BASE_URL=http://127.0.0.1:8090 DEPLOYMATE_EMAIL=... DEPLOYMATE_PASSWORD=... ./testdata/e2e.sh
set -euo pipefail

BASE="${DEPLOYMATE_BASE_URL:-http://127.0.0.1:8090}"
EMAIL="${DEPLOYMATE_EMAIL:-owner@deploymate.dev}"
PASSWORD="${DEPLOYMATE_PASSWORD:-devpassword123}"
JAR="$(mktemp)"
trap 'rm -f "$JAR"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }

# 1. Login
curl -sf -c "$JAR" -o /dev/null -d "email=$EMAIL&password=$PASSWORD" "$BASE/login" \
  || fail "login"

csrf() { curl -s -b "$JAR" "$1" | grep -oE 'csrf_token" value="[A-Za-z0-9_-]+' | head -1 | cut -d'"' -f3; }

# 2. Create project + app
CSRF="$(csrf "$BASE/projects")"
PROJ="e2e-$(date +%s)"
curl -sf -b "$JAR" -o /dev/null -d "name=$PROJ&csrf_token=$CSRF" "$BASE/projects" || fail "create project"
SLUG="$(echo "$PROJ" | tr '[:upper:]' '[:lower:]')"
curl -sf -b "$JAR" -o /dev/null -d "name=Web&csrf_token=$CSRF" "$BASE/projects/$SLUG/apps" || fail "create app"
echo "ok: project + app created"

# 3. Deploy nginx (image is usually already local)
CSRF="$(csrf "$BASE/apps/web")"
curl -sf -b "$JAR" -o /dev/null --max-time 300 -d "image=nginx:alpine&port=80&csrf_token=$CSRF" \
  "$BASE/apps/web/deploy" || fail "deploy"
echo "ok: deploy accepted"

# 4. Verify the container came up on the private network
for i in $(seq 1 30); do
  if docker inspect dm-web --format '{{.State.Running}}' 2>/dev/null | grep -q true; then
    echo "ok: dm-web running"
    exit 0
  fi
  sleep 2
done
fail "container never started"
