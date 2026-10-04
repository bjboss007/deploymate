#!/usr/bin/env bash
# DeployMate installer — downloads the latest release for this machine, checks
# its SHA-256 against the release's checksums.txt, then runs the bootstrap that
# sets up Docker, the firewall, the service user, Traefik and the systemd unit.
#
#   curl -fsSL https://raw.githubusercontent.com/bjboss007/deploymate/main/deploy/install.sh \
#     | sudo DEPLOYMATE_LE_EMAIL=you@example.com bash
#
# Needs a fresh Ubuntu 24.04 LTS (amd64 or arm64) and root. Read the script
# first if you like — it is short, and everything it runs ships in the archive.
#
# Environment:
#   DEPLOYMATE_LE_EMAIL   your email, for Let's Encrypt (recommended)
#   DEPLOYMATE_VERSION    a tag such as v0.1.0 (default: latest)
#   DEPLOYMATE_REPO       owner/name (default: bjboss007/deploymate)
#   DEPLOYMATE_BASE_URL   download from here instead (tests, mirrors)
#   DEPLOYMATE_DRY_RUN=1  download and verify, but do not run the bootstrap
set -euo pipefail

REPO="${DEPLOYMATE_REPO:-bjboss007/deploymate}"
VERSION="${DEPLOYMATE_VERSION:-latest}"
DRY_RUN="${DEPLOYMATE_DRY_RUN:-0}"

say() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = "Linux" ] || die "the server installer is for Linux (Ubuntu 24.04). On a Mac, build with 'make build'."
if [ "$DRY_RUN" != "1" ] && [ "$(id -u)" -ne 0 ]; then
  die "run as root: curl … | sudo bash"
fi

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU architecture: $(uname -m) (amd64 and arm64 are built)" ;;
esac

if [ -n "${DEPLOYMATE_BASE_URL:-}" ]; then
  BASE="$DEPLOYMATE_BASE_URL"
elif [ "$VERSION" = "latest" ]; then
  BASE="https://github.com/$REPO/releases/latest/download"
else
  BASE="https://github.com/$REPO/releases/download/$VERSION"
fi

ARCHIVE="deploymate_linux_${ARCH}.tar.gz"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

say "downloading $ARCHIVE ($VERSION)"
curl -fsSL "$BASE/$ARCHIVE" -o "$TMP/$ARCHIVE" || die "could not download $BASE/$ARCHIVE"
curl -fsSL "$BASE/checksums.txt" -o "$TMP/checksums.txt" || die "could not download checksums.txt"

say "verifying checksum"
WANT="$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*" f { print $1 }' "$TMP/checksums.txt")"
[ -n "$WANT" ] || die "$ARCHIVE is not listed in checksums.txt"
GOT="$(sha256sum "$TMP/$ARCHIVE" | awk '{print $1}')"
[ "$WANT" = "$GOT" ] || die "checksum mismatch for $ARCHIVE — refusing to install (expected $WANT, got $GOT)"

say "unpacking"
mkdir -p "$TMP/pkg"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP/pkg"
[ -x "$TMP/pkg/deploymate" ] && [ -f "$TMP/pkg/deploy/bootstrap.sh" ] || die "the archive is not what the installer expects"

if [ "$DRY_RUN" = "1" ]; then
  say "dry run: verified, not installing. Version: $("$TMP/pkg/deploymate" version 2>/dev/null || echo unknown)"
  exit 0
fi

say "running the bootstrap"
bash "$TMP/pkg/deploy/bootstrap.sh" "$TMP/pkg/deploymate"
