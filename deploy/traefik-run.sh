#!/usr/bin/env bash
# (Re)create DeployMate's Traefik container. One place for the flags, used by
# bootstrap.sh (first install) and `deploymate update` (moving a server to the
# Traefik version a release pins).
#
#   traefik-run.sh image                  print the pinned image and exit
#   traefik-run.sh apply [DATA_DIR] [STATIC_TEMPLATE]
#                                         pull it, replace the container, verify
#
# Traefik runs on the HOST network. The dashboard is a systemd service on 127.0.0.1:8080, not
# a container, so only a host-network Traefik can reach it to give it a domain; the docker
# provider still reaches every app by its address on deploymate-net. Ports 80/443 are bound
# directly (the firewall rules for them apply to Traefik now). With STATIC_TEMPLATE, the static
# config is regenerated from it, keeping the Let's Encrypt email already in use (the previous
# file is kept as traefik.yml.bak).
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
NET="${TRAEFIK_NETWORK:-host}"
PORTS="${TRAEFIK_PORTS:-}"

if [[ "${1:-}" == "image" ]]; then
  echo "$TRAEFIK_IMAGE"
  exit 0
fi
[[ "${1:-}" == "apply" ]] || { echo "usage: $0 image | apply [DATA_DIR] [STATIC_TEMPLATE]" >&2; exit 2; }

DATA_DIR="${2:-/var/lib/deploymate}"
TEMPLATE="${3:-}"
[[ -f "$DATA_DIR/traefik.yml" ]] || { echo "no $DATA_DIR/traefik.yml: this server's Traefik was not set up by DeployMate, leaving it alone" >&2; exit 3; }

mkdir -p "$DATA_DIR/traefik-dynamic"
chmod 755 "$DATA_DIR/traefik-dynamic"
chown deploymate:deploymate "$DATA_DIR/traefik-dynamic" 2>/dev/null || true  # DeployMate writes dashboard.yml here

CONFIG_CHANGED=0
if [[ -n "$TEMPLATE" ]]; then
  EMAIL="$(grep -m1 -E '^[[:space:]]*email:' "$DATA_DIR/traefik.yml" | awk '{print $2}' || true)"
  [[ -n "$EMAIL" && "$EMAIL" != "ADMIN_EMAIL" ]] || EMAIL="${DEPLOYMATE_LE_EMAIL:-admin@example.com}"
  NEW="$(sed "s/ADMIN_EMAIL/$EMAIL/" "$TEMPLATE")"
  if [[ "$NEW" != "$(cat "$DATA_DIR/traefik.yml")" ]]; then
    cp "$DATA_DIR/traefik.yml" "$DATA_DIR/traefik.yml.bak"
    printf '%s\n' "$NEW" > "$DATA_DIR/traefik.yml"
    CONFIG_CHANGED=1
    echo "==> traefik.yml updated (previous kept as traefik.yml.bak; Let's Encrypt email: $EMAIL)"
  fi
fi

run_traefik() {
  # shellcheck disable=SC2086
  docker run -d --name "$NAME" --restart unless-stopped \
    --network "$NET" \
    $PORTS \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    -v "$DATA_DIR/letsencrypt:/letsencrypt" \
    -v "$DATA_DIR/traefik.yml:/etc/traefik/traefik.yml:ro" \
    -v "$DATA_DIR/traefik-dynamic:/etc/traefik/dynamic:ro" \
    "$1" >/dev/null
}

PREVIOUS=""
PREV_NET=""
if docker inspect "$NAME" >/dev/null 2>&1; then
  PREVIOUS="$(docker inspect -f '{{.Config.Image}}' "$NAME")"
  PREV_NET="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$NAME")"
  # Already right only if: same image, running, on the wanted network, with the dynamic
  # directory mounted, and the static config unchanged.
  if [[ "$PREVIOUS" == "$TRAEFIK_IMAGE" && "$CONFIG_CHANGED" == 0 ]] \
     && [[ "$(docker inspect -f '{{.State.Running}} {{.HostConfig.NetworkMode}}' "$NAME")" == "true $NET" ]] \
     && docker inspect -f '{{range .Mounts}}{{.Destination}} {{end}}' "$NAME" | grep -q '/etc/traefik/dynamic'; then
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
    [[ "$CONFIG_CHANGED" == 1 ]] && cp "$DATA_DIR/traefik.yml.bak" "$DATA_DIR/traefik.yml"
    # Back as it was: a server that had Traefik on the bridge network gets its published ports back.
    if [[ "$PREV_NET" == "host" ]]; then
      run_traefik "$PREVIOUS"
    else
      docker run -d --name "$NAME" --restart unless-stopped --network "$PREV_NET" -p 80:80 -p 443:443 \
        -v /var/run/docker.sock:/var/run/docker.sock:ro -v "$DATA_DIR/letsencrypt:/letsencrypt" \
        -v "$DATA_DIR/traefik.yml:/etc/traefik/traefik.yml:ro" "$PREVIOUS" >/dev/null
    fi
  fi
  exit 1
fi
echo "Traefik is $TRAEFIK_IMAGE"
