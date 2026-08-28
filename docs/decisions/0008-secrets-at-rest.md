# 0008 — Secrets at rest: XChaCha20-Poly1305 under a 0600 key file

- **Date:** 2026-08-28
- **Status:** accepted

## Context

Env var values, git deploy keys, webhook secrets, and database passwords
must not sit in plaintext in the metadata DB (Coolify/Dokploy store them
plaintext; we can do better without building a vault).

## Decision

**XChaCha20-Poly1305** with a random 24-byte nonce per encryption;
envelope format `v1:<base64 nonce>:<base64 ciphertext>`. The master key is
32 random bytes in `<data>/keys/root.key` (0600), created on first boot.
Encryption/decryption lives in `internal/crypto`; the store persists
opaque envelopes and the HTTP layer owns the key.

Rules:
- Everything is encrypted uniformly — `is_secret` only controls UI masking,
  not storage.
- Service credentials are generated once, stored encrypted, and decrypted
  only when building container env or connection URLs.
- Decryption failures are logged and skipped, never fatal to a deploy.

## Consequences

- A leaked `data.db` is worthless without the key file; a leaked key file
  without the DB is worthless. Both live in the same data dir — this is
  at-rest hygiene, not a hardware-isolated KMS.
- The `v1:` prefix leaves room for key rotation (a `v2:` envelope could
  reference a second key) — improvement backlog.

## Alternatives

- **Plaintext + file perms** (Coolify/Dokploy) — rejected; AEAD is barely
  more code.
- **age / SOPS / HashiCorp Vault** — better key management, but another
  operational dependency for a single box; revisit at multi-server.
