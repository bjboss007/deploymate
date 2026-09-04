# Database backups to external storage — specification

**Status:** implemented (Sep 2026) — Postgres MVP shipped end to end
(`make e2e-backup` green on a throwaway server with a local destination).
One deliberate wording correction below: the repo's AEAD is
XChaCha20-Poly1305 (ADR 0008), not AES-GCM — the crypto.md-free
"AEAD under the server key" phrasing in this spec should read that way
wherever "AES" appears (three spots: `key_enc`, the blob encryption, and
the security note). Remaining follow-ups stay in the backlog: MySQL/Redis
dumpers and manifest-declared backup config ("Infra manifest seeding").
Related: `docs/knowledge/database.md`, `docs/specs/infra-manifest.md`.

## Problem

Named volumes are not backups: they sit on the same disk as the server,
and a disk failure (or a mistaken `docker volume rm`) takes the data
with it. DeployMate provisions databases (Postgres/MySQL/Redis) whose
ports are deliberately unpublished — nothing outside the Docker network
can reach them — so no host-side dump tool exists today either. The
goal: an **opt-in, per-service** scheduled snapshot of each provisioned
database, encrypted, uploaded **off-box** to Cloudflare R2, listable and
restorable from the UI.

Owner decisions (Sep 2026):

1. **Storage:** Cloudflare R2 (S3-compatible), configured as named
   destinations.
2. **Opt-in per service** — nothing backs up until a service is told to.
3. **Defaults:** daily at 02:00, keep the newest **14** backups.
4. **Keying:** each service gets a **dedicated backup key** (not the
   server `encKey`), generated at opt-in and downloadable once per
   need, so a wiped server doesn't wipe the key to its own off-box
   backups.
5. **MVP scope: Postgres only** (`pg_dump -Fc` + `pg_restore`);
   MySQL/Redis use the same machinery with different commands.

Configurability is a first-class requirement: a service's backup can
pick **what time / how often** (cron), **how long to keep**, and
**which storage destination** to use.

## Design

### Per-service backup config

A 1:1 row keyed by service id (migration **0014**):

| field | default | meaning |
|---|---|---|
| `enabled` | `0` | opt-in switch |
| `schedule` | `0 2 * * *` | 5-field cron, **server-local time** — time *and* frequency |
| `keep` | `14` | keep the newest N objects for this service, prune older |
| `destination` | `default` | which configured storage destination to use |
| `key_enc` | — | the service's backup key, XChaCha20-Poly1305'd with the server key (the existing secrets-at-rest path, ADR 0008 — `internal/crypto.Encrypt`) |
| `last_run_at` | — | persisted so a restart never double-runs a window |

Opting in (UI toggle or API) generates the key (`crypto/rand` 32 bytes),
stores it encrypted, and records a `backup_enabled` event. The service
page offers the **key for download** (plain hex, same trust gate as
webhook-secret viewing) — the owner keeps a copy off-box; without it a
backup made by a dead server is unreadable, *by design*.

Presets in the UI fill the cron field (Daily 02:00; Daily midnight;
Hourly at :13; weekly Sun 03:00) — free-form cron is allowed for the
"really configurable" cases. Unparseable cron is rejected at save time.

### Storage destinations (configurable "where")

Destinations are registered at server boot from env blocks, one per
destination id:

```
DEPLOYMATE_BACKUP_DEST_<ID>_TYPE=s3|local
# s3:
DEPLOYMATE_BACKUP_DEST_<ID>_ENDPOINT=…     # R2: https://<acct>.r2.cloudflarestorage.com
DEPLOYMATE_BACKUP_DEST_<ID>_ACCESS_KEY=…
DEPLOYMATE_BACKUP_DEST_<ID>_SECRET_KEY=…
DEPLOYMATE_BACKUP_DEST_<ID>_BUCKET=…
# local (dev + e2e only — never a backup of record):
DEPLOYMATE_BACKUP_DEST_<ID>_DIR=…
```

- The per-service `destination` field picks among them; an id that is
  not configured is a save-time error. The id `default` is the fallback
  for existing rows.
- **MVP ships the `s3` type pointed at R2** (one S3-compatible client —
  candidate: minio-go, pinned per ADR 0012) plus the `local` type so
  dev and the e2e suite never touch the real bucket.
- Object layout, flat per service: `backups/{service_slug}/{ts}-{nonce}.dump.enc`
  with a sibling `….meta.json` (plaintext, no data): service slug,
  type, image, taken-at, size, sha256 of the blob, key id (first bytes
  of the service key, hex) so a downloaded key can be matched to
  objects. The bucket listing *is* the catalog — no mirror table to
  drift.

### Snapshot execution

Runs **inside the service's own container** via the runtime's exec seam
(the same one the provisioner's readiness probe uses) — ports stay
unpublished:

1. `docker exec` the dump to a temp file in the container:
   `pg_dump -Fc -U {user} -d {db} -f /tmp/dm-backup.dump` (custom
   format: compressed, and restore is `pg_restore`, not SQL replay).
   Credentials decrypt from the service's encrypted store — the same
   `{user}/{db}` pair `DATABASE_URL` assembly uses, nothing new to
   manage.
2. Copy the file out, delete the temp.
3. gzip it, encrypt **XChaCha20-Poly1305** with the service's backup key
   (random nonce, `internal/crypto.EncryptBytes` — raw nonce‖ciphertext,
   no base64 envelope, so binary blobs stay binary), upload, verify
   sha256, clean the container-side temp file.

*Runtime prerequisite (shipped with this round):* `runtime.Runtime` gained
`WriteFile`/`ReadFile` (tar-packed `CopyToContainer`/`CopyFromContainer`
via the docker SDK) and `ExecEnv` (exec with env). `Exec` itself is
untouched — dumps are file round-trips, not stdout streams.

### The scheduler

A backup scheduler starts with the server: a 1-minute tick evaluates
every enabled config and runs those whose `next(schedule, last_run_at)
≤ now` (cron parsed with a small pinned library — candidate
robfig/cron — used for `Next()` only; the driver is ours so `last_run_at`
persists and a server restart can't double-fire a window).

- **Single-flight per service:** a run in progress skips that service's
  ticks.
- **Stopped service:** skip + `backup_skipped` event (a stopped
  container has nothing to exec into; DeployMate won't start a service
  just to back it up).
- **Missed windows** (server was down) are not back-filled; the next
  cron firing takes it — and the **"Back up now"** button on the
  service page covers the urgent gap (same code path, immediate run).
- Failure: `backup_failed` event + an alert through the existing
  dispatcher (ADR 0014). Success: `backup_ok` event. The events
  timeline is the run history.

### Retention

After each successful upload, list the service's prefix and delete all
but the newest `keep` objects (the ts in the key orders them). Prune
failures are events/warnings, never a failure of the fresh backup.

### Restore (Postgres, MVP)

From the service page → Backups list (newest first: time, size, sha) →
Restore on one → **typed confirmation** (destructive, the app-delete
pattern) → `restore_started` event. The flow:

1. Download the object, verify sha256, decrypt with the service key to
   a staging file under the data dir.
2. Copy the dump into the container.
3. Prepare a clean target: terminate `app`-DB connections, drop and
   recreate the database (the service user is the image superuser),
   then `pg_restore -Fc -d {db} --no-owner` — a clean slate, no
   stale-object collisions.
4. `restore_ok|failed` event + alert on failure. Apps keep running
   through the window; their clients reconnect (the demo apps already
   degrade/recover gracefully).

Restoring an old dump into a **newer-major** Postgres (the
`latest`-floating case) may fail — restoring to a same-version service
is the documented boundary (matching the existing "pin in production"
rule).

## Security

- Backups are client-side encrypted before they leave the box; R2 never
  sees plaintext (R2's own server-side encryption is belt-and-suspenders).
  The AEAD authenticates — tampered blobs fail decryption, and sha256 is
  checked against the meta before any restore touches data.
- The per-service key lives encrypted-at-rest under the server `encKey`
  (existing ADR 0008 machinery) and is downloadable by the logged-in
  owner. Object meta carries no SQL data, no credentials, no key
  material.
- Destination credentials live in server env (host trust, like the
  Cloudflare vars); uploads go over HTTPS.
- Restore is destructive and human-gated (typed slug + events trail).
- A backup of a database whose service row dies is still restorable —
  the object key + meta identify it; restoring to a *different* server
  is the documented manual path below.

## Verification

Unit: cron parse/validation + defaults, retention prune math (keep-N by
key ts), config store round-trip incl. key generate/encrypt/decrypt,
encryption + meta round-trip, restore command building (terminate /
drop / recreate / restore ordering), scheduler due/skip/first-run logic,
single-flight.

E2e: `make e2e-backup` in the throwaway pattern (spare port, scratch
data dir, **local destination**):

1. Provision a real Postgres service (template path), insert rows.
2. Opt in via config, run "Back up now" → object in the local
   destination, events recorded, size > 0.
3. Delete the data (drop the table), restore from the object → rows are
   back.
4. Retention: with `keep` = 1, a second backup prunes the first.
5. Stopped service → skip event; bad destination id → rejected at save.
6. Cleanup: throwaway server, container, volume, destination dir.

## Out of scope (v1)

- MySQL/Redis dumpers (same machinery; `mysqldump`, Redis `SAVE`/RDB
  swap restore — a container-level restore, since RDB replaces while
  nothing runs).
- Manifest-declared backup config (`deploymate.yml`) — the "Infra
  manifest seeding" backlog item builds on this config surface.
- WAL archiving / point-in-time recovery; catch-up of missed windows.
- Restore onto a different server (the manual path: download the
  `.dump.enc` + matching downloaded key, decrypt with a small script —
  a `deploymate` CLI subcommand is a natural follow-up).
- Health-style "backup overdue" monitoring beyond the failure alert.
