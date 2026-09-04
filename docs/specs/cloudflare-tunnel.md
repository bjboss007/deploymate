# Cloudflare Tunnel configuration — specification

**Status:** live (Aug 2026) · Domain: `getmerchanttech.com` · Tunnel: `deploymate`

## Why a named tunnel

Quick tunnels (`cloudflared tunnel --url …`) were the dev bootstrap: zero
setup, but **no uptime guarantee, a new random URL per restart, and
silent death** (our first one stopped accepting connections without the
process even exiting — GitHub webhooks reported "failed to connect to
host" while cloudflared looked healthy).

A **named tunnel** has a stable identity (UUID + credentials file) and
routes DNS records in the user's own Cloudflare account. The hostname
survives process restarts, machine reboots, and IP changes.

## Topology

```
                    Cloudflare edge (TLS terminates here)
                              │
        ┌─────────────────────┼──────────────────────────┐
        │                     │                          │
 dm.getmerchanttech.com   *.dm.getmerchanttech.com   (any other hostname)
        │                     │                          │
        └──────────┬──────────┘                         404
                   │ (cloudflared, local process, preserves Host header)
                   ▼
        DeployMate :8090 on 127.0.0.1
        │  Host == {slug}.dm.getmerchanttech.com
        ▼
        /preview-style reverse proxy → 127.0.0.1:{loopback port}
```

- **Dashboard + webhooks**: `dm.getmerchanttech.com` → origin 8090.
- **Apps**: `{slug}.dm.getmerchanttech.com` → same origin; DeployMate
  routes by `Host` header (middleware in `internal/httpserver/server.go`)
  — no per-app ingress rules needed.
- TLS is Cloudflare's problem (edge certs). The origin stays plain HTTP
  on loopback. No Traefik, no Let's Encrypt, no open ports.

## Configuration files

`~/.cloudflared/config.yml` (user machine; templated copy in
`deploy/cloudflared/config.yml.example`):

```yaml
tunnel: <tunnel-id>
credentials-file: ~/.cloudflared/<tunnel-id>.json   # keep secret

ingress:
  - hostname: dm.getmerchanttech.com
    service: http://127.0.0.1:8090
  - hostname: "*.dm.getmerchanttech.com"
    service: http://127.0.0.1:8090          # DeployMate routes by Host
  - service: http_status:404
```

## DNS records

- `dm.getmerchanttech.com` → tunnel CNAME
- `*.dm.getmerchanttech.com` → tunnel CNAME
- **Per-app records** — required because **Cloudflare's free plan does
  not issue edge certificates for wildcard hostnames**. A wildcard
  record routes traffic, but TLS handshakes fail until each individual
  hostname has its own record (universal SSL covers single-level
  subdomains).

Per-app records are created **automatically** when an app is created:
the DeployMate server POSTs a proxied CNAME
(`{slug}.{previewHost}` → `{tunnel-id}.cfargotunnel.com`) to the
Cloudflare API (`internal/dns`, `NewCloudflare`). Error 81053 ("record
already exists") is treated as success, so manual records and
delete-and-recreate both work. Deleting an app removes its records the
same way: a list-by-name GET (`?name={host}`) then a DELETE per id —
no matching record, or a 404 racing a concurrent delete, is success.
Best-effort in both directions; the manual fallback remains
`cloudflared tunnel route dns deploymate <hostname>`.

## DeployMate side

- `DEPLOYMATE_PREVIEW_HOST=dm.getmerchanttech.com` — enables Host-header
  routing and switches the app page's Access panel to show
  `http://{slug}.dm.getmerchanttech.com` as the preview URL (the
  `/preview/{slug}` dashboard path keeps working). The scheme stays
  plain http because edge cert issuance lags app creation; browsers
  auto-upgrade to https, which works once the cert lands.
- `DEPLOYMATE_CLOUDFLARE_API_TOKEN` + `DEPLOYMATE_CLOUDFLARE_ZONE_ID` +
  `DEPLOYMATE_CLOUDFLARE_TUNNEL_ID` — enable **auto-DNS** (all three
  must be set): each new app's preview CNAME is created via the
  Cloudflare API on app creation and removed on app deletion. Token
  scope: `Zone.DNS:Edit` on the preview zone. Best-effort in both
  directions: failures never block app create/delete; they are logged,
  recorded as a `dns_record_failed` event (create), and surfaced as a
  warning flash on the redirect.
- The Host-route middleware is **public by design** (no session) — that
  is the point of a preview URL. Apps without a port, or not running,
  return 404/503.

## Webhook endpoint

`https://dm.getmerchanttech.com/hooks/{source-id}` — HMAC-verified as
always. GitHub webhook config (repo → Settings → Webhooks): URL + the
secret shown on the app's Git panel, content type `application/json`,
push events.

**Incident note (Aug 2026):** after rotating the secret, all deliveries
got 401s. Root cause: the rotation wrote the secret with a trailing
newline (shell `echo` → file → encrypt), while GitHub stored the clean
48-char value; the handler computed HMACs with 49 bytes. Symptom was
indistinguishable from a wrong secret. Lesson: trim every secret that
enters the store, and the delivery history on GitHub
(Settings → Webhooks → Recent Deliveries) is the first diagnostic.

## Process persistence (macOS dev machine)

The tunnel currently runs as a detached process (`cloudflared tunnel run
deploymate`). For boot persistence on the laptop server or any Mac:

```sh
sudo cloudflared service install   # launchd, reads /etc/cloudflared/config.yml
```

On the Ubuntu server, the same named tunnel credentials can be copied
(`cert.pem` + `<tunnel-id>.json`) and run under systemd
(`cloudflared service install` ships a unit) — the tunnel is a property
of the Cloudflare account, not the machine.
