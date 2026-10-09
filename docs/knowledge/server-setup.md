# Turning a laptop into the DeployMate server

The full path from "old Windows laptop" to "your PaaS runs here". Target
machine: any Intel/AMD 64-bit laptop, 8 GB+ RAM (4 GB works with swap —
see the caveats), SSD, Ethernet cable.
(Apple Silicon MacBooks are not recommended for this — Asahi Linux is not
server-grade for Docker.)

## 1. Install Ubuntu 24.04 LTS

1. Download the **Ubuntu 24.04.x LTS desktop or server ISO** from
   ubuntu.com/download.
2. Make a bootable USB: Rufus (Windows) or balenaEtcher. GPT/UEFI mode.
3. Boot the laptop from USB (BIOS boot menu, usually F12/F9/Esc).
4. Install: "Erase disk and install Ubuntu", minimal installation is fine.
   - During install: enable **OpenSSH server** if offered (desktop image
     asks; server image always installs it).
   - Hostname suggestion: `deploymate-host`.
5. Reboot into the installed system.

## 2. Make it behave like a server, not a laptop

```sh
sudo apt update && sudo apt upgrade -y
# copy deploy/laptop-server.sh to the laptop, then:
sudo bash laptop-server.sh
```

That disables lid-close suspend, masks sleep/hibernate, and blanks the
console after 5 minutes. Physically: keep the lid open or closed as you
like (logind now ignores it), keep it on a hard surface, and expect the
battery to degrade from always-on charging — laptops tolerate this, but
it's one reason real servers exist.

## 3. Network — the two shapes

**A. Port-forward + domain (recommended; everything we built works as
designed):**

1. In your router: give the laptop a DHCP reservation (static LAN IP).
2. Port-forward TCP 80 and 443 to that IP.
3. Buy/point a domain's A record at your **public IP** (most home IPs
   change — check your ISP for a static IP or use a DDNS updater; DuckDNS
   is free and works).
4. Note: some ISPs use CGNAT (no public IP at all). Test: your router's
   WAN IP should equal the IP that `curl ifconfig.me` shows. If they
   differ, use shape B.

**B. Cloudflare tunnel (no port forwarding, works behind CGNAT):**

A named tunnel (free) gives stable public URLs with TLS at Cloudflare's
edge. Note: our Traefik Let's Encrypt flow assumes public 80/443, so with
a tunnel use Cloudflare's own certificates for app domains (SSL mode
Full) and set `DEPLOYMATE_LE_MODE=off` so Traefik stops asking Let's Encrypt for certificates it cannot get. The dashboard + webhooks
work identically. This is the quick-tunnel setup we used in dev, made
permanent:

```sh
cloudflared tunnel login            # once, browser auth
cloudflared tunnel create deploymate
cloudflared tunnel route dns deploymate dm.example.com
sudo cloudflared service install   # runs as a systemd service
```

## 4. Install DeployMate (the same bootstrap as any server)

```sh
# build the binary on any 64-bit machine (or the laptop itself after
# installing Go); on macOS:
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o deploymate-linux ./cmd/deploymate

scp deploymate-linux deploy/ root@<laptop-ip>:~/
ssh root@<laptop-ip>   # then:
DEPLOYMATE_LE_EMAIL=you@example.com bash deploy/bootstrap.sh deploymate-linux
sudo -u deploymate DEPLOYMATE_DATA_DIR=/var/lib/deploymate /usr/local/bin/deploymate setup-admin
```

bootstrap.sh installs Docker CE + buildx, the railpack binary, the
BuildKit daemon (`dm-buildkit`), Traefik, ufw (22/80/443), the deploymate
user, and the systemd service.

## 5. Migrate from the dev machine

What moves and what doesn't:

| Data | How |
|---|---|
| DeployMate metadata (`data.db`, `keys/`) | `scp` both into `/var/lib/deploymate/` — projects, apps, services, env vars, and the encryption key all move. Stop deploymate first, copy, then start. |
| Container images (`deploymate/apps/...`) | **Do not move** — Docker Desktop's images live in a VM. Redeploy each app on the server; the deployment history stays intact. |
| Database data (Postgres/MySQL volumes) | Fresh-provision the services on the server, then `pg_dump`/`mysqldump` from the old containers and restore (or just start clean if it's demo data). |
| Git sources + webhooks | Move with the DB. Update each GitHub webhook URL from the tunnel URL to `https://<server-domain>/hooks/<id>` — shown on every Git panel. |
| LE mode | Set `DEPLOYMATE_LE_MODE=production` in `/etc/systemd/system/deploymate.service`, then `systemctl daemon-reload && systemctl restart deploymate`. |

## 6. Smoke test the server

```sh
systemctl status deploymate traefik     # both active
curl -I https://dm.example.com          # 200 via Traefik + LE cert
make e2e                                # or manual: login, deploy, preview URL
sudo reboot                            # everything must come back on its own
```

The reboot test is the one that tells you it's a real server now.

## Home-server caveats (honest version)

- **4 GB RAM is viable, but only with swap.** The idle stack (Docker +
  Traefik + deploymate + a couple of Postgres services) fits in 4 GB fine;
  the risk is *build* time — railpack/BuildKit Node builds (`npm run
  build`, vite) spike to 1–2 GB, and on 4 GB with no swap the OOM killer
  shoots the build (or deploymate itself) mid-deploy, which looks like
  random build failures. `bootstrap.sh` provisions a 4 GB swap file with
  `vm.swappiness=10` by default (override `SWAP_SIZE_GB`, or `=0` to skip);
  that lets a build page instead of dying. Other mitigations already in
  place: the worker builds one app at a time (serial), and per-app memory
  limits are derived automatically (ADR 0015). Still, **don't run the
  dev/stage/prod triples from the dogfood demo on a 4 GB box** (that was 12
  containers) — one or two apps plus their services is the realistic
  ceiling. 8 GB+ removes the constraint.
- **No ECC RAM, no redundant power, no IPMI.** A laptop is fine for your
  own projects and demos; it is not a production SLA. If DeployMate ever
  serves paying customers, rent a Hetzner box — the whole stack moves
  with the same bootstrap.sh + data-dir copy.
- **Disk**: `docker system df` monthly; the nightly image retention helps
  but BuildKit caches grow — `docker exec dm-buildkit buildctl prune`.
- **WiFi**: don't. Ethernet or nothing; a WiFi blip mid-deploy just fails
  a build, but flaky WiFi will make uptime charts ugly.
- **Updates**: `sudo apt upgrade` on a schedule; deploymate + railpack
  upgrades are version bumps in bootstrap.sh (railpack) and the binary
  (deploymate).

## Dashboard on its own domain (v0.3)

Re-run the installer with `DEPLOYMATE_DASHBOARD_HOST=dm.example.com` (and `DEPLOYMATE_LE_MODE=production` once
DNS points at the server), or add the variable to the unit with `systemctl edit deploymate` and restart. The
details, and why Traefik runs on the host network, are in `docs/improvements.md` (server-side dashboard routing).
