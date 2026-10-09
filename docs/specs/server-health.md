# Server health (the "Server" page)

Shows the health of the machine DeployMate runs on: storage, memory, CPU and the things that make a
self-hosted server stop working. Planned 2026-10-09, built in three steps.

## Step 1 — snapshot (done 2026-10-09)

- `internal/hostinfo`: reads `/proc` and `/sys` (CPU from `/proc/stat` deltas, load, `/proc/meminfo`,
  memory pressure (PSI), disks via statfs, network from `/proc/net/dev` skipping lo/veth/docker/br-,
  uptime, OS, kernel, reboot-required, hottest thermal zone, battery/AC). The reader takes a root
  directory, so tests run on a fixture tree. No Linux → `Supported=false` and the page says so.
- `hostinfo.Evaluate` → one verdict: ok / warning / critical with plain-words findings. Thresholds
  live in `verdict.go` (disk free <15% warn, <5% crit; memory available <10% / <4%; memory pressure
  ≥10%; load5 > 1.5×cores; CPU ≥90%; temperature ≥80/90 °C; on battery; reboot needed; Docker or
  Traefik not running).
- `/server` page (top-bar icon; refreshes itself every 15 s via HTMX, which gets only the body),
  `GET /api/v1/server` (read scope; numbers only — no paths, hostnames or OS strings) and the
  `server_status` MCP tool. Also on the page: Docker's disk breakdown (reuses the `/stats` panel's
  data), the busiest apps (latest `metrics` sample per running app), DeployMate version, database size.
- Demo instance: `DEPLOYMATE_DEMO_HOST=1` swaps in a fictional healthy host (`internal/demo/host.go`)
  and hides the real Docker panel; the website tour has a "Server" tab captured from it.

## Step 2 — history (planned)

`host_metrics` table sampled every 60 s (CPU, memory used, swap, disk %, load, network, temperature),
7-day retention like app metrics, 24-hour sparklines on the page, per-app contribution over time.

## Step 3 — alerts and cleanup (planned)

Host alert rules through the existing alert system (disk, memory, sustained CPU, temperature, Docker
down), firing only on a change of state like the certificate alerts. A "Clean up" action that prunes
build cache and unused images after showing the reclaimable size; never volumes or anything a running
app uses; dashboard-only, not exposed to agents at first.

## Notes

- Single server: only the machine DeployMate runs on.
- On a VM or container the numbers are what that machine sees (cgroup limits can differ from the host).
