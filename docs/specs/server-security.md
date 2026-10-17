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
- **B. Host hardening (built 2026-10-16)** — `sudo deploymate harden` (`internal/harden`, `cmd/deploymate/harden.go`).
  Dry run by default; `--apply` changes things. Profile is auto-detected (`DEPLOYMATE_LE_MODE=off` = tunnel) or set
  with `--profile`. Every change is recorded in `<data>/harden-state.json` with how to reverse it.
  - *Both:* unattended-upgrades + `20auto-upgrades`; an sshd drop-in `00-deploymate.conf` (first value wins, so it
    beats cloud-init's `50-`): `MaxAuthTries 4`, `PermitRootLogin no` (only when run through sudo as a normal user),
    `PasswordAuthentication no` only when the user has an authorized key **and** sshd logged a key login for them in
    the last 30 days. The config is checked with `sshd -t` before reload and restored if rejected.
  - *Public:* `ufw limit 22/tcp` (added before the open rule is removed), allow 80/443, default deny incoming, enable;
    fail2ban with an sshd jail.
  - *Tunnel:* close 80/443; port 22 is closed to everyone but `--ssh-from <CIDR>` (left alone without it); no
    fail2ban (every connection looks local). The tunnel path itself comes in over loopback, which ufw does not filter.
  - *Safety:* firewall and SSH changes are "risky": before applying, `systemd-run` starts a timer
    (`deploymate-harden-revert`, `--timeout` minutes, default 5) that runs `harden --revert`; the timer lives in
    systemd, so it fires even if the SSH session dies. `--confirm` stops it (keep), `--undo` reverses everything.
    If the timer cannot be started nothing is changed. Packages are never uninstalled by undo. Existing user rules
    are never touched except the plain 22/80/443 allow rules being replaced as described.
  - *Left out on purpose:* kernel sysctl tuning (small gain, non-zero risk), installing ufw (enabling a firewall on
    a box that has none is the owner's call; harden says so and stops).
  - *Tested:* unit tests with a fake host (ordering, key gate, rollback of a rejected sshd config, undo); the drop-in
    validated by real OpenSSH 9 in Ubuntu 24.04. **Not tested on a real host:** ufw changes and the revert timer.
- **C. DeployMate itself (built 2026-10-16, without 2FA).**
  - *Headers* (`internal/httpserver/headers.go`) on every dashboard response except `/preview/` (a proxied app keeps
    its own): CSP (`frame-ancestors 'none'`, `base-uri`, `object-src 'none'`, `connect-src 'self'`; scripts and styles
    keep `'unsafe-inline'` because pages still use inline scripts and handlers, so it does not stop injected inline
    script), `X-Frame-Options: DENY`, `nosniff`, `Referrer-Policy: same-origin`, `Permissions-Policy`, and HSTS (1 year,
    no subdomains) only over HTTPS.
  - *Sessions* last 7 days (was 30; existing ones keep their expiry). **Sign out everywhere** on the Sign-ins page
    (`POST /logout-all`, `Store.DeleteSessionsForUser`).
  - *Sign-in audit:* successful and refused sign-ins go to the audit log (`token_name` "dashboard", action `login`,
    target = email, detail = address). Attempts refused by the limiter are not logged (a scan would flood the table).
    New alert event **`login_blocked`** ("Repeated failed dashboard sign-ins"): one notification when an address or
    email reaches its limit (`loginLimiter.fail` reports the crossing), not one per attempt.
  - *Sign-ins page (`/signins`, sidebar item under Server; moved off the Security page 2026-10-17 because that page was getting crowded; the Security page links to it):* counts for the last 24 hours (signed in, failed,
    blocked) and active sessions, 7-day totals, and the latest 20 attempts with a **new address** tag on a successful
    sign-in from an address that never signed in before. Refused attempts are recorded; a **blocked** entry is written
    once when an address or email reaches its limit. Sign-in rows are kept 90 days (pruned every 6 hours) and no longer
    appear in the API tokens page's activity list.
  - *Client address in tunnel mode:* `Server.clientAddr` trusts `CF-Connecting-IP`, but only when
    `DEPLOYMATE_LE_MODE=off` and the peer is loopback (the tunnel is then the only way in); otherwise the old
    last-`X-Forwarded-For` rule applies. Without this every visitor behind the tunnel shared one rate-limit bucket.
  - *Not done:* **optional TOTP 2FA** (own round: secret storage, enrolment, recovery codes, login step), alert on a
    sign-in from a never-seen address, a fail2ban jail for the dashboard (useless behind Cloudflare, and needs
    Traefik's access log on public servers).
- **E. Docs:** hardening guide, Cloudflare Access for SSH and the dashboard, what to rotate after a suspected breach.

## Safety rules
Nothing that can lock the owner out is applied without a revert timer. Nothing changes at install except the safe
parts. Every change is shown first and logged.
