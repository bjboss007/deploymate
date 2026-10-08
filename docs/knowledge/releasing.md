# Releasing

A release is a git tag. Pushing `v<major>.<minor>.<patch>` runs
`.github/workflows/release.yml`, which tests, builds four archives with
`deploy/package.sh`, writes `checksums.txt`, and publishes a GitHub release.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

## What is in a release

| File | For |
|---|---|
| `deploymate_linux_amd64.tar.gz`, `deploymate_linux_arm64.tar.gz` | servers — the static binary + `deploy/bootstrap.sh`, `deploy/deploymate.service`, `deploy/traefik/static.yml`, LICENSE, NOTICE |
| `deploymate_darwin_arm64.tar.gz`, `deploymate_darwin_amd64.tar.gz` | a laptop running `deploymate mcp` against a remote server |
| `checksums.txt` | `sha256` of each archive; `deploy/install.sh` verifies it before running anything |

Archive names are **stable** (no version in the file name) so
`…/releases/latest/download/<name>` is a permanent URL.

## Rules that keep the installer honest

- The binary is pure Go (`CGO_ENABLED=0`): one file runs on any distro of that
  architecture. If a dependency ever needs cgo, the archives stop being portable —
  stop and reconsider.
- `-X main.version=<tag>` stamps `deploymate version` and the MCP server identity.
- `install.sh` aborts on a checksum mismatch, an unlisted archive, a non-Linux
  host or a non-root run. `DEPLOYMATE_DRY_RUN=1` downloads and verifies without
  installing; `DEPLOYMATE_BASE_URL=file:///dir` points it at local archives (how it
  was tested, in an `ubuntu:24.04` container: good, tampered, unlisted, non-root).
- CI (`ci.yml`) fails if `templ generate` would change a committed `_templ.go`
  file, runs vet + tests, and cross-compiles linux/amd64, linux/arm64, darwin/arm64.

## `deploymate update` (servers)

`cmd/deploymate/update.go` + `internal/updater`. Downloads `deploymate_linux_<arch>.tar.gz` and
`checksums.txt` from the release (latest page redirect for the tag, no API token), verifies,
extracts only `deploymate` and `deploy/deploymate.service`, smoke-tests the new binary
(`version`), stops the service, copies `data.db` → `data.db.pre-update`, moves the old binary
to `.prev`, installs the new one (and the unit if it changed, keeping this install's data
dir), starts it and polls `/healthz`. Any failure after the stop restores binary, database and
unit and starts the old version. It deliberately does **not** re-run `bootstrap.sh` (that
recreates Traefik and resets the Let's Encrypt email), so changes to Traefik's config or to
Docker/host setup still need a manual step — say so in the release notes when they exist.
Unit tests cover swap, rollback, checksum and extraction; the systemd/real-restart path has
only been exercised by hand.

## First release checklist

1. `go test ./...` green on `main`; `progress.md` current.
2. Tag, push, watch the Release run.
3. On a clean Ubuntu 24.04 VM: run the README one-liner, create the admin, open
   the dashboard, deploy an image app. (Not yet done on real hardware — the
   installer's download/verify path is tested; the bootstrap itself ran on the
   author's server.)
