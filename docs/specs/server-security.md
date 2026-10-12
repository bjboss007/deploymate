# Server security

Planned 2026-10-12 with the owner after reading their real server (ufw, listening ports, sshd). Goal: close what an
attacker could reach, in the order of real exposure, without ever locking the owner out.

## Threat models
- **Public server** (ports open): fail2ban, firewall rules and SSH policy matter most.
- **Behind a tunnel** (the owner's: Cloudflare Tunnel, `DEPLOYMATE_LE_MODE=off`): every connection to sshd and
  Traefik arrives from loopback, so fail2ban cannot see real addresses; the protection is the tunnel provider's
  access policy (the owner's SSH hostname has a Cloudflare Access application) and, on the host, closing 22/80/443
  inbound entirely (cloudflared connects out). The audit adapts to `TunnelMode`.

## Findings on the owner's server (2026-10-12)
ufw on with default deny (22, 80, 443 allowed); only Traefik and sshd listen publicly, except **Remote Desktop on
0.0.0.0:3389** (blocked by ufw only); sshd: passwords on, root key-only; no fail2ban; no security updates pending.
In the code: **Redis services have no password and every container shares one Docker network**; containers run with
Docker defaults (no `no-new-privileges`); no security headers on the dashboard; Traefik holds the Docker socket; no
login audit or alerts; 30-day sessions; no 2FA.

## Phases
- **A. Audit (done 2026-10-12).** `internal/security`: read-only checks run as the service user (world-readable
  files, `/proc/net/tcp*`, `systemctl is-active`) plus what DeployMate knows (containers via `Inspect`, Redis
  passwords): firewall on/default-drop, open ports beyond SSH/web (databases are critical), tunnel-mode advice
  (close 22/80/443), SSH password and root login (OpenSSH first-value-wins, Match blocks ignored), fail2ban,
  automatic and pending security updates, reboot needed, docker-group members, dashboard bind address, data-dir
  mode, Redis without a password, containers publishing on a non-loopback address (Docker bypasses ufw) or
  privileged / holding the Docker socket, containers without `no-new-privileges`. Each check has a level, what was
  found and the exact fix. Surfaces: `/security` page (link from the Server page), `GET /api/v1/security`, MCP
  `security_status`. Limits: ufw's rules and the effective sshd config can only be read as root, so the audit says
  to run `sudo ufw status verbose` / `sudo sshd -T`.
- **D. Containers (first half done 2026-10-12).**
  - *Redis password.* New Redis services get a generated password (`redis-server --requirepass`, `REDIS_URL`
    with the escaped password, a readiness probe that passes the password as an argument and looks for `PONG`
    because `redis-cli` exits 0 on NOAUTH, usage probe through `REDISCLI_AUTH`). Services created before this
    stored an empty password and are **not touched** (running apps hold the old URL in their environment): their
    page shows "This Redis has no password" with **Add a password**, which stores a password, re-creates the
    container (data volume kept) and redeploys the apps of the same project and environment that receive it.
    The audit flags password-less Redis until then. **Remove password** on a protected Redis is the way back for an
    app that cannot use one (same flow in reverse). Apps also receive `REDIS_HOST/PORT/PASSWORD` (and
    `DATABASE_*`, `MYSQL_*`) next to the URL, for frameworks that take them apart.
  - *Container hardening.* Every container DeployMate creates gets `no-new-privileges`, `cap-drop NET_RAW, MKNOD,
    SETFCAP` and a 4096 pids limit (`runtime.Docker.Create`). Verified on real Docker with Postgres 16, MySQL 8.4,
    Redis 7, nginx (Postgres 18's image could not be downloaded here; it uses the same entrypoint). Cost: no
    `ping` inside containers. Existing containers pick it up when they are next deployed or provisioned.
  - *Not done, deliberately:* **one Docker network per project** and **Traefik behind a Docker socket proxy**.
    Per-project networks touch routing (Traefik, now on the host network, would need a
    `traefik.docker.network` label on every container, replicas, swaps and previews), need a migration of
    running containers, and matter most when several untrusted projects share a server; the socket proxy can
    break all routing. Both need their own round and test on the real server.
- **B. Host hardening, opt-in** (`deploymate harden`, dry run by default, idempotent, with a revert timer for
  anything that can lock out): fail2ban + `ufw limit 22` (public servers), unattended-upgrades, SSH policy (no
  root login; key-only only after a key is confirmed with a second connection), tunnel profile (close 22/80/443
  inbound, keep SSH from the LAN), kernel network settings.
- **C. DeployMate itself:** security headers (CSP, frame, content-type, HSTS behind HTTPS), shorter sessions and
  "sign out everywhere", login audit events and alerts (failed bursts, new address), optional TOTP, a fail2ban
  jail for dashboard sign-in (needs Traefik's access log; useless behind Cloudflare), optionally trusting
  `CF-Connecting-IP` for the login limiter in tunnel mode.
- **E. Docs:** hardening guide, Cloudflare Access for SSH and the dashboard, what to rotate after a suspected breach.

## Safety rules
Nothing that can lock the owner out is applied without a revert timer. Nothing changes at install except the safe
parts. Every change is shown first and logged.
