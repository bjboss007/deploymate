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

## First release checklist

1. `go test ./...` green on `main`; `progress.md` current.
2. Tag, push, watch the Release run.
3. On a clean Ubuntu 24.04 VM: run the README one-liner, create the admin, open
   the dashboard, deploy an image app. (Not yet done on real hardware — the
   installer's download/verify path is tested; the bootstrap itself ran on the
   author's server.)
