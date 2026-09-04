# Database

## Engine & pragmas

SQLite via `modernc.org/sqlite` (pure Go). DSN pragmas:
`busy_timeout=5000`, `journal_mode=WAL`, `foreign_keys=1`.
`SetMaxOpenConns(1)` — one connection serves everyone; writes serialize,
and with a single worker + batched monitor inserts this is never a
contention point. Migrations: `pressly/goose`, embedded
(`internal/migrations/*.sql`, numbered), applied at startup before the
server listens. The migration dir passed to goose is `"."` because
`//go:embed *.sql` puts files at the FS root.

## Tables

| Table | Purpose | Notable columns |
|---|---|---|
| `users` | accounts (single owner today) | `role` (`owner` now, more later) |
| `sessions` | browser sessions | `token_hash` (SHA-256 of cookie value), `csrf_token`, `expires_at` |
| `projects` | grouping for apps + services | unique `slug` |
| `git_sources` | connected repos | `provider`, `repo_url`, encrypted deploy key + webhook secret, `default_branch` |
| `apps` | runnable things (manual or git) | `git_source_id` (nullable FK), `image`, `port`, `entrypoint`/`command` (manual-deploy overrides, raw whitespace-split strings; `''` = image default), `status`, `current_deployment_id` |
| `env_vars` | per-app environment | `value_enc` (always encrypted), `is_secret` (UI masking only), UNIQUE(app_id, key) |
| `deployments` | deploy history **and** the worker queue | `kind` (deploy/rollback/manual), `status` (queued/building/running/failed), `image_tag`, `error` |
| `build_logs` | build output per deployment | `seq`, `stream`, append-only |
| `images` | built image tags (rollback registry) | keep newest 5 per app |
| `domains` | hostname → app routing | `tls_status` (pending/active/failed — informational) |
| `services` | databases & caches | `type`, `volume_name`, `port`, `status` |
| `service_credentials` | generated passwords | encrypted values |
| `metrics` | 5 s resource samples | pruned after 7 d |
| `uptime_checks` | 30 s domain probes | pruned after 30 d |

## Conventions that matter

- **IDs**: random 128-bit hex strings (`store.NewID`). Timestamps: RFC3339Nano
  UTC strings (`store.Now`).
- **NULL vs empty string**: nullable FK columns (`apps.git_source_id`)
  reject `""` at insert (FK constraint) and fail Go `string` scans at read —
  write with `NULL`, scan into `sql.NullString`. Both sides are handled in
  `internal/store/apps.go`; keep the pattern for future nullable columns.
- **Encrypted columns**: the store persists and returns opaque envelopes
  (`v1:<nonce>:<ct>`); only the HTTP layer (`internal/crypto`) encrypts and
  decrypts. Never store plaintext.
- **`deployments` is a queue**: rows with `status='queued'` are work.
  Claim = `UPDATE ... SET status='building' WHERE id=? AND status='queued'`;
  if 0 rows affected, someone else claimed it.

## Service credential semantics (read before touching)

- Passwords are generated once at first provision and **never shown**;
  `service_credentials` holds them encrypted.
- Postgres/MySQL apply `POSTGRES_PASSWORD`/`MYSQL_*` **only at initdb**
  (first boot of the data volume). If the DeployMate DB row is lost but the
  named volume survives, the stored password and the live database diverge
  — recovery is manual (docker exec + ALTER USER). Same as any managed
  provider: the volume *is* the source of truth for live data.
- Deleting a service keeps its named volume (`dm-svc-<slug>-data`) — data
  deletion is an explicit act.
- Connection URLs injected into apps: `DATABASE_URL` (postgres,
  `?sslmode=disable` — private network, no TLS), `MYSQL_URL`, `REDIS_URL`.
  Host is the container name (`dm-svc-<slug>`), resolvable on
  `deploymate-net`.
