#!/usr/bin/env bash
# (Re)create DeployMate's Traefik container. One place for the flags, used by
# bootstrap.sh (first install) and `deploymate update` (moving a server to the
# Traefik version a release pins).
#
#   traefik-run.sh image                  print the pinned image and exit
#   traefik-run.sh apply [DATA_DIR]       pull it, replace the container, verify
#
# `apply` pulls first, so a failed pull changes nothing, and if the new container
# does not stay up it puts the previous image back. Apps are unreachable for the
# few seconds between the old container stopping and the new one starting.
set -euo pipefail

# v3.7.14: label contract (TLS routers, certresolver, replicas sharing one service, healthcheck
# ejection) re-tested 2026-10-09 against v3.3; supports Docker Engine 29 (API 1.44 and up).
TRAEFIK_IMAGE="${TRAEFIK_IMAGE:-traefik:v3.7.14}"
# Overridable so the script can be tested beside a real Traefik without touching it.
NAME="${TRAEFIK_CONTAINER:-traefik}"
NET="${DEPLOYMATE_NET:-deploymate-net}"
PORTS="${TRAEFIK_PORTS:--p 80:80 -p 443:443}"

if [[ "${1:-}" == "image" ]]; then
  echo "$TRAEFIK_IMAGE"
  exit 0
fi
[[ "${1:-}" == "apply" ]] || { echo "usage: $0 image | apply [DATA_DIR]" >&2; exit 2; }

DATA_DIR="${2:-/var/lib/deploymate}"
[[ -f "$DATA_DIR/traefik.yml" ]] || { echo "no $DATA_DIR/traefik.yml: this server's Traefik was not set up by DeployMate, leaving it alone" >&2; exit 3; }

run_traefik() {
  # shellcheck disable=SC2086
  docker run -d --name "$NAME" --restart unless-stopped \
    --network "$NET" \
    $PORTS \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    -v "$DATA_DIR/letsencrypt:/letsencrypt" \
    -v "$DATA_DIR/traefik.yml:/etc/traefik/traefik.yml:ro" \
    "$1" >/dev/null
}

PREVIOUS=""
if docker inspect "$NAME" >/dev/null 2>&1; then
  PREVIOUS="$(docker inspect -f '{{.Config.Image}}' "$NAME")"
  if [[ "$PREVIOUS" == "$TRAEFIK_IMAGE" ]] && [[ "$(docker inspect -f '{{.State.Running}}' "$NAME")" == "true" ]]; then
    echo "Traefik is already $TRAEFIK_IMAGE"
    exit 0
  fi
fi

echo "==> pulling $TRAEFIK_IMAGE"
docker pull "$TRAEFIK_IMAGE" >/dev/null

echo "==> replacing Traefik ${PREVIOUS:+($PREVIOUS) }with $TRAEFIK_IMAGE"
docker rm -f "$NAME" >/dev/null 2>&1 || true
run_traefik "$TRAEFIK_IMAGE"
sleep 5
# A crashing container with a restart policy still reports "running" between restarts,
# so require that it is up, not restarting, and has not restarted at all.
if [[ "$(docker inspect -f '{{.State.Running}} {{.State.Restarting}} {{.RestartCount}}' "$NAME" 2>/dev/null)" != "true false 0" ]]; then
  echo "the new Traefik did not stay up; log:" >&2
  docker logs "$NAME" 2>&1 | tail -15 >&2 || true
  if [[ -n "$PREVIOUS" ]]; then
    echo "==> putting $PREVIOUS back" >&2
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    run_traefik "$PREVIOUS"
  fi
  exit 1
fi
echo "Traefik is $TRAEFIK_IMAGE"
