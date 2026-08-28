# 0012 — Dependency pins & CLI tooling that bit us

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Three ecosystem issues discovered during the build each forced a pin or a
tool choice. They're recorded together because the rule is the same:
**prefer the boring, battle-tested interface over the newest one.**

## Decisions

1. **Docker Go SDK pinned to `github.com/docker/docker@v28.5.2+incompatible`.**
   Newer module lines split into `github.com/moby/moby` + a separate
   `api` module with moved packages (`client`, `errdefs`, `stdcopy` all
   relocated); v28 is the last line where the classic import paths live
   in one module. Revisit when the split stabilizes.

2. **Deploy keys via the `ssh-keygen` CLI** (see 0005) — `x/crypto/ssh`'s
   `MarshalPrivateKey` produced keys that `ssh-keygen -y` rejected.

3. **Container stats CPU% uses `online_cpus`, falling back to
   `len(percpu_usage)`** — Docker Desktop omits `percpu_usage` entirely,
   and the naive formula silently reports 0%.

4. **`ImagePull` is never called for images that may exist locally** —
   the daemon's pull endpoint hits the registry even when the image is
   present and fails for local-only tags; callers must `HasImage` first.

5. **goose migration dir is the embedded FS root** (`.`), not a
   subdirectory — `//go:embed *.sql` places files at the FS root.

## Consequences

- Upgrading the Docker SDK or x/crypto requires re-testing the three
  integration points above (keys, stats, pull semantics).
- All of these are documented in `docs/knowledge/troubleshooting.md`
  with the actual error signatures, so future maintainers can
  recognize them in minutes instead of hours.
