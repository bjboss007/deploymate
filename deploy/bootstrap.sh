#!/usr/bin/env bash
# DeployMate bootstrap — turns a fresh Ubuntu 24.04 LTS server into a
# DeployMate host: Docker CE, firewall, deploymate user, systemd service,
# and the Traefik reverse proxy (the only service with public ports).
#
# Usage (as root, on the server):
#   DEPLOYMATE_LE_EMAIL=you@example.com bash bootstrap.sh /path/to/deploymate
#   # optional: BINARY_URL=https://... to download the binary instead
#
# Idempotent: safe to re-run.
set -euo pipefail

BINARY_PATH="${1:-}"
LE_EMAIL="${DEPLOYMATE_LE_EMAIL:-admin@example.com}"
DATA_DIR=/var/lib/deploymate
SERVICE_USER=deploymate

if [[ $EUID -ne 0 ]]; then
  echo "run as root: sudo bash bootstrap.sh /path/to/deploymate" >&2
  exit 1
fi

echo "==> installing docker-ce + buildx"
if ! command -v docker >/dev/null 2>&1; then
  apt-get update -y
  apt-get install -y ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
    > /etc/apt/sources.list.d/docker.list
  apt-get update -y
  apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
systemctl enable --now docker

echo "==> firewall: ssh (22), http (80), https (443) only"
ufw allow OpenSSH
ufw allow 80/tcp
ufw allow 443/tcp
ufw --force enable

echo "==> swap file (low-RAM hosts: keeps railpack/BuildKit builds off the OOM killer)"
# 4 GB boxes are under the 8 GB target; Node/vite builds spike to 1-2 GB and
# the OOM killer shoots the build (or deploymate itself) without swap to page
# into. Skip if any swap is already active, or SWAP_SIZE_GB=0 to opt out.
SWAP_SIZE_GB="${SWAP_SIZE_GB:-4}"
SWAP_FILE=/swapfile
if [[ "$SWAP_SIZE_GB" != "0" ]] && [[ -z "$(swapon --show --noheadings)" ]]; then
  if [[ ! -e "$SWAP_FILE" ]]; then
    # fallocate can produce a file swapon rejects on some filesystems; dd is safe.
    fallocate -l "${SWAP_SIZE_GB}G" "$SWAP_FILE" 2>/dev/null \
      || dd if=/dev/zero of="$SWAP_FILE" bs=1M count="$((SWAP_SIZE_GB * 1024))" status=none
    chmod 600 "$SWAP_FILE"
    mkswap "$SWAP_FILE" >/dev/null
  fi
  swapon "$SWAP_FILE"
  grep -qxF "$SWAP_FILE none swap sw 0 0" /etc/fstab \
    || echo "$SWAP_FILE none swap sw 0 0" >> /etc/fstab
  # Prefer RAM; only page under real pressure — swap is an OOM backstop, not a workhorse.
  echo "vm.swappiness=10" > /etc/sysctl.d/99-deploymate-swap.conf
  sysctl -q vm.swappiness=10
  echo "    ${SWAP_SIZE_GB}G swap active (vm.swappiness=10)"
else
  echo "    swap already present or disabled (SWAP_SIZE_GB=$SWAP_SIZE_GB) — skipping"
fi

echo "==> service user + data dirs"
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
usermod -aG docker "$SERVICE_USER"
mkdir -p "$DATA_DIR"/{keys,builds,repos,letsencrypt}
chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
chmod 700 "$DATA_DIR/keys"

echo "==> shared network for apps, databases, and traefik"
docker network inspect deploymate-net >/dev/null 2>&1 || docker network create deploymate-net

echo "==> deploymate binary"
if [[ -n "$BINARY_PATH" ]]; then
  install -m 0755 "$BINARY_PATH" /usr/local/bin/deploymate
elif [[ -n "${BINARY_URL:-}" ]]; then
  curl -fsSL "$BINARY_URL" -o /usr/local/bin/deploymate
  chmod 0755 /usr/local/bin/deploymate
else
  echo "provide a binary path (arg 1) or BINARY_URL" >&2
  exit 1
fi

echo "==> railpack (runtime-selected builds, no Dockerfile needed)"
RAILPACK_VERSION="${RAILPACK_VERSION:-v0.38.0}"
if [ ! -x /usr/local/bin/railpack ]; then
  RAILPACK_ARCH="$(uname -m)"
  [ "$RAILPACK_ARCH" = "aarch64" ] && RAILPACK_ARCH="arm64"
  curl -fsSL "https://github.com/railwayapp/railpack/releases/download/${RAILPACK_VERSION}/railpack-${RAILPACK_VERSION}-${RAILPACK_ARCH}-unknown-linux-musl.tar.gz" \
    | tar xz -C /usr/local/bin railpack
fi

echo "==> buildkit daemon for railpack (docker-container://dm-buildkit)"
if ! docker ps -a --format '{{.Names}}' | grep -qx dm-buildkit; then
  docker run -d --name dm-buildkit --restart unless-stopped --privileged moby/buildkit:latest
fi
docker start dm-buildkit >/dev/null 2>&1 || true

echo "==> traefik static config"
sed "s/ADMIN_EMAIL/$LE_EMAIL/" "$(dirname "$0")/traefik/static.yml" > "$DATA_DIR/traefik.yml"

if docker ps -a --format '{{.Names}}' | grep -qx traefik; then
  docker rm -f traefik >/dev/null
fi
docker run -d --name traefik --restart unless-stopped \
  --network deploymate-net \
  -p 80:80 -p 443:443 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v "$DATA_DIR/letsencrypt:/letsencrypt" \
  -v "$DATA_DIR/traefik.yml:/etc/traefik/traefik.yml:ro" \
  traefik:v3.3

echo "==> systemd service"
sed "s|/var/lib/deploymate|$DATA_DIR|" "$(dirname "$0")/deploymate.service" > /etc/systemd/system/deploymate.service
systemctl daemon-reload
systemctl enable --now deploymate

echo
echo "done. next steps:"
echo "  1. point an A record at this server, e.g. dm.example.com"
echo "  2. create your login:  sudo -u $SERVICE_USER DEPLOYMATE_DATA_DIR=$DATA_DIR /usr/local/bin/deploymate setup-admin"
echo "  3. open the dashboard through traefik once a domain is attached, or"
echo "     ssh -L 8080:127.0.0.1:8080 $SERVICE_USER@$(hostname -I | awk '{print $1}')"
