# Unattended install (managed servers)

Written 2026-10-17 for the commercial direction the owner chose: sell DeployMate **and** its management to developers,
small agencies and businesses, starting with the customer's own cloud account. The repo stays public (Apache-2.0).
This spec covers the one piece every version of that business needs: a fresh server that installs DeployMate by
itself, hardens itself, and tells us it is ready, with nobody typing on it. Nothing here is built yet.

## Goal and non-goals
**Goal:** from "an empty Ubuntu 24.04 server with cloud-init" to "dashboard reachable, admin account exists, host
hardened, we hold a management token, and the control side was told" in one boot, in under ~10 minutes, safe to
re-run.

**Non-goals (later rounds):** the control plane itself (a service that holds customer records and provider tokens),
billing, creating the server through a provider API, multi-server fleets, teams/SSO inside DeployMate.

## What exists today
- `deploy/install.sh` → `bootstrap.sh`: Docker, swap, service user, Traefik, systemd unit, env drop-in
  (`DEPLOYMATE_DASHBOARD_HOST`, `DEPLOYMATE_LE_MODE`, `DEPLOYMATE_PREVIEW_HOST`). Already non-interactive.
- `deploymate setup-admin` reads `DEPLOYMATE_SETUP_EMAIL` / `DEPLOYMATE_SETUP_PASSWORD`.
- `deploymate harden` (dry run by default) with public and tunnel profiles.
- API tokens (`read`, `deploy`, `provision`) and the MCP server; both are created only from the dashboard.

## Gaps this spec closes
| # | Gap | Why it blocks unattended use |
|---|---|---|
| 1 | `bootstrap.sh` always opens 22/80/443 in ufw | wrong for tunnel servers; the later `harden` run then has to undo it |
| 2 | The admin password has to be passed in clear | user-data is readable from the instance metadata service and provider consoles |
| 3 | No CLI to mint an API token | we need a management token without a browser |
| 4 | `harden` wants to see a key login in the SSH log | a fresh machine has none; cannot run unattended |
| 5 | `harden --apply` starts a revert timer that waits for a human | nobody will run `--confirm` |
| 6 | Nothing tells the outside the server is ready | the control side would poll blindly |
| 7 | cloudflared is not installed or configured by the installer | tunnel servers (the owner's own setup) need it |
| 8 | Backup destination is a manual env block | each customer needs their own bucket/prefix from day one |

## Design

### The one input: a provisioning file
cloud-init writes `/etc/deploymate/provision.env` (root, mode 0600) and runs
`curl -fsSL https://deploymate.link/install | bash -s -- --unattended`. The installer reads that file; every key is
optional except the first two.

```
DEPLOYMATE_ADMIN_EMAIL=owner@customer.com        # required
DEPLOYMATE_ADMIN_PASSWORD_HASH=$argon2id$…       # required unless _SETUP_LINK=1 (below)
DEPLOYMATE_VERSION=v0.8.0                        # pin; default latest
DEPLOYMATE_DASHBOARD_HOST=dm.customer.com
DEPLOYMATE_LE_EMAIL=ops@customer.com
DEPLOYMATE_LE_MODE=production|off               # off = tunnel
DEPLOYMATE_TUNNEL_TOKEN=…                        # tunnel mode: installs and starts cloudflared
DEPLOYMATE_SSH_FROM=203.0.113.0/24               # tunnel mode: network that may keep port 22
DEPLOYMATE_BACKUP_DEST_DEFAULT_*=…               # one block, written to the unit drop-in
DEPLOYMATE_READY_URL=https://control.example/ready/<id>
DEPLOYMATE_READY_TOKEN=…                         # one-time, authenticates the callback
```

### Decisions
1. **No clear-text password.** The control side generates the password, shows it to the customer once, and puts only
   its **hash** in user-data (`setup-admin` learns `DEPLOYMATE_SETUP_PASSWORD_HASH`; it stores the hash as is).
   Alternative for hand-provisioned servers: `DEPLOYMATE_SETUP_LINK=1` prints a one-time `/setup?token=…` URL to the
   console log and `/etc/deploymate/setup-url` instead; the first visitor sets the password and the token dies.
2. **Management token without a browser:** `deploymate token create --name managed --scope provision [--days N]`
   prints the plaintext once (same store as the dashboard's tokens). The installer runs it and sends the token in the
   ready callback, **not** in user-data. The customer sees and can revoke it on the Tokens page.
3. **Bootstrap becomes profile aware:** `--unattended` and `DEPLOYMATE_LE_MODE=off` skip the ufw 22/80/443 allows;
   the harden step then sets the rules. Public servers keep today's rules.
4. **`harden` gets two non-interactive switches:** `--trust-ssh-key` (skip the "key login seen in the log" gate when
   an authorized key exists for the login user and the provider put it there) and `--no-timer` (no revert timer,
   because the installer is the only session and there is no human to confirm). Both are refused unless an
   authorized key exists, so a server never ends up with passwords off and no key.
5. **Ready callback:** after `/healthz` and `GET /api/v1/whoami` succeed, `POST DEPLOYMATE_READY_URL` with
   `{version, hostname, public_ip, dashboard_url, tunnel: bool, management_token, harden: {applied, skipped[]}}`,
   `Authorization: Bearer <READY_TOKEN>`, 5 retries over ~2 minutes. The token is single-use on the control side.
   A failed callback does not fail the install; the result is also written to `/var/lib/deploymate/install-result.json`.
6. **Idempotent:** every step checks before acting; re-running with the same file repairs, never duplicates. The
   provisioning file is deleted at the end (unless `DEPLOYMATE_KEEP_PROVISION=1`), because it holds the tunnel and
   ready tokens.
7. **cloudflared:** apt repo install, `cloudflared service install <token>`, verified with `systemctl is-active`
   before the firewall closes 80/443. Ordering matters: the tunnel must be up before inbound is closed.

### Order of operations
1. Preflight (Ubuntu 24.04, root, disk ≥ 20 GB, RAM ≥ 2 GB, outbound HTTPS) — fail early with one clear line.
2. Download + checksum-verify the release (as today).
3. Bootstrap (Docker, swap, user, Traefik, unit + drop-in).
4. Start DeployMate, wait for `/healthz`.
5. Create the admin; mint the management token.
6. Tunnel: install cloudflared, wait for it to connect.
7. `harden --apply --trust-ssh-key --no-timer` with the right profile; verify SSH still answers on loopback and,
   for public servers, that port 22 still listens, **before** reporting ready.
8. Ready callback; remove the provisioning file.

If step 7 fails the install still reports ready with `harden.applied=false` and the reason; a half-hardened server is
reported, not hidden. If step 6 fails the firewall is never closed.

## Security notes
- user-data is not secret: it carries a password **hash**, a one-time ready token and (tunnel mode) the tunnel token.
  The tunnel token can be rotated in Cloudflare; the others expire on use. Say so in the customer docs.
- The management token travels once, over HTTPS, to an endpoint authenticated by the one-time ready token.
- The ready callback must not follow redirects or accept plain HTTP.
- Provider API tokens (bring-your-own-cloud) belong to the control plane; they never reach the customer's server.

## Verification
- Unit: provisioning-file parser (required keys, bad values, quoting), `token create`, hash-based `setup-admin`, the
  `harden` switches (refuse without an authorized key), ready-callback retry/backoff.
- Script tests with `DEPLOYMATE_DRY_RUN=1` and a fake `BASE_URL` (the existing installer pattern).
- **End to end on a real VM** (Multipass or a throwaway provider instance): run cloud-init with a public profile and a
  tunnel profile; assert dashboard, login with the supplied hash, token works, ufw rules, sshd settings, callback
  received, second run changes nothing. This is the only test that proves the ordering, so it gates the release.

## Build order
1. **Done 2026-10-17:** `deploymate token create --name N --scope read|deploy|provision [--days N] [--email E]` (plaintext on
   stdout only, rest on stderr, 20-token cap, refuses to run as root against a service-owned database);
   `DEPLOYMATE_SETUP_PASSWORD_HASH` for `setup-admin` and for the serve-time bootstrap (validated argon2id, wins over
   the clear password; an existing owner is never touched); `deploymate hash-password` (reads one line from stdin);
   `harden --trust-ssh-key --no-timer --user NAME` (the two switches are refused unless that user has an authorized
   key; `--user` replaces `SUDO_USER` for cloud-init runs). Not in this step: `DEPLOYMATE_SUPPORT_KEY`, which belongs
   with the installer in step 2.
2. Installer `--unattended` + provisioning file + profile-aware firewall + cloudflared.
3. Ready callback + `install-result.json`.
4. A cloud-init template per provider documented on the website; the VM end-to-end test.
5. Only then the control plane (separate spec).

## Decisions (owner, 2026-10-17)
1. **Reaching a managed server:** through the customer's own tunnel hostname, with a Cloudflare Access service token on
   their account. No agent for now; revisit if it becomes a pain (the agent is the more robust option).
2. **The tunnel lives in the customer's Cloudflare account.** They stay in control; we never hold their only way in.
3. **Providers:** provider-neutral script; Hetzner and DigitalOcean first for docs. Revisit when a first customer names
   their provider.
4. **Support access:** a visible, revocable SSH key in the customer's `authorized_keys` (labelled, listed on the
   Security page, removable), never a shared secret. Without that key we work only through the dashboard API and the
   management token. The unattended installer must therefore be able to add (and a flag to omit) this key.

Consequences for the build: the ready callback carries the Access service-token hostname instead of a public address;
the installer takes `DEPLOYMATE_SUPPORT_KEY` (public key, optional) and writes it with a comment such as
`deploymate-support` so it is recognisable; the Security audit lists it.

See `docs/knowledge/legal-checklist.md` for the legal questions that gate the first paid customer.
