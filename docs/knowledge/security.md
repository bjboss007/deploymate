# Security posture

Single-user, single-server product — the threat model is: **an attacker on
the internet must not reach anything except the proxy, and must not forge
deploys or read secrets.**

## Attack surface

| Surface | Control |
|---|---|
| Host ports | ufw allows 22/80/443 only; **only Traefik publishes publicly-reachable ports** — app/db containers live on the internal bridge. Apps additionally publish their port to **127.0.0.1 loopback only** (for `/preview/<slug>`); loopback bindings are unreachable from the internet |
| Dashboard | argon2id password (PHC-encoded), httpOnly SameSite session cookie, per-session CSRF token on every POST, constant-time comparisons |
| Webhooks | HMAC-SHA256 (`X-Hub-Signature-256`) / `X-GitLab-Token`, raw-body read once, 1 MiB cap, delivery-ID dedup (24 h), branch filter; handlers never execute repo code |
| Secrets | XChaCha20-Poly1305 at rest (deploy keys, webhook secrets, GitHub API tokens for prebuilt deploys, env values, DB passwords); key file 0600; DB creds never displayed |
| Deploy keys | ed25519, per-repo, read-only intent (add as read-only deploy key in the forge), 0600 temp file per clone, removed after |
| User code | runs only inside containers; builds run in BuildKit containers; no `--privileged`; no host mounts |
| Docker socket | held by the deploymate host process (docker group) and Traefik (read-only); **not** mounted into app containers. Hardening idea: `tecnativa/docker-socket-proxy` in front of Traefik (improvements) |
| TLS | automatic via Let's Encrypt; staging resolver by default so dev mistakes can't hit production rate limits; production is an explicit flag |
| systemd unit | `NoNewPrivileges`, `ProtectSystem=strict` + `ReadWritePaths=/var/lib/deploymate`, `ProtectHome`, `PrivateTmp`, `UMask=0077` |

## Residual risks (accepted, documented)

- **GitHub API token (prebuilt deploys)**: a fine-grained token with
  *Actions: read* can list runs/artifacts and download artifacts — it cannot
  read code or start workflows (verified). Its **repository scope is the
  owner's choice**: an *All repositories* token reads the build artifacts of
  every private repo it can see, and a leak of the database **and** key
  file together would expose them. Create it with *Only select
  repositories*. The artifact download's signed storage URL is never logged
  and never receives the token. Webhook gates refuse pull-request and fork
  runs, so attacker-built artifacts cannot reach the deploy path — and the
  dashboard's "Deploy latest successful run" goes through the same gates.
  The UI token field is write-only (never rendered or echoed), and Test
  connection warns when the token can read other private repos.
- **Docker socket = root-equivalent**: the platform process can do
  anything on the host. Accepted for a personal server; the blast radius
  is "someone who already owns your deploymate login owns the box" —
  which is true of any self-hosted PaaS.
- **SQLite + key file share one directory**: encryption at rest protects
  against DB leaks (backups, disk disposal), not against an attacker
  with host access.
- **SSE endpoints and dashboard are same-origin cookie sessions** — fine
  behind Traefik TLS.
- **Webhook dedup is in-memory**: a server restart within a provider's
  retry window can double-queue; the result is a redundant deploy of the
  same SHA, not a security issue.

## Secrets handling rules for contributors

1. New secret fields → encrypted columns via `internal/crypto`, never
   plaintext, never logged.
2. Logging: `slog` may log IDs/names/errors; it must never log values of
   encrypted fields, webhook secrets, or passwords (decrypted or not).
3. UI: secrets render masked (`is_secret`); connection URLs and webhook
   secrets shown on the app page are for the owner only — don't add them
   to public logs or error messages.
4. Anything that executes on the host (git, buildx, ssh) gets its inputs
   from DeployMate-owned config only — never directly from unverified
   webhook payloads.
