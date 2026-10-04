#!/usr/bin/env bash
# Build one release archive: deploymate_<os>_<arch>.tar.gz holding the static
# binary plus what the installer needs (bootstrap script, systemd unit, Traefik
# config) and the licence files.
#
# Usage: deploy/package.sh <version> <goos> <goarch> [outdir]
set -euo pipefail

VERSION="${1:?version, e.g. v0.1.0}"
GOOS="${2:?goos, e.g. linux}"
GOARCH="${3:?goarch, e.g. amd64}"
OUT="${4:-dist}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

export COPYFILE_DISABLE=1 # macOS tar: no AppleDouble/xattr headers in the archive
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
mkdir -p "$STAGE/deploy/traefik" "$OUT"

# Pure Go (modernc SQLite, Docker SDK over the socket): no cgo, so one static
# binary runs on any distro of that architecture.
(cd "$ROOT" && CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$STAGE/deploymate" ./cmd/deploymate)

cp "$ROOT/deploy/bootstrap.sh" "$ROOT/deploy/deploymate.service" "$STAGE/deploy/"
cp "$ROOT/deploy/traefik/static.yml" "$STAGE/deploy/traefik/"
cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$ROOT/README.md" "$STAGE/"
chmod 0755 "$STAGE/deploymate" "$STAGE/deploy/bootstrap.sh"

ARCHIVE="$OUT/deploymate_${GOOS}_${GOARCH}.tar.gz"
tar -czf "$ARCHIVE" -C "$STAGE" .
echo "$ARCHIVE"
