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

## Step 2 — history and host alerts (done 2026-10-09)

- `host_metrics` (migration 0022): one reading a minute (CPU, memory in use, data-disk %, load, network
  rates, temperature), kept 7 days (pruned hourly with the other metrics). Sampled by the monitor
  (`monitor/host.go`) with its own `hostinfo.Collector`, so rates are minute averages.
- `GET /server/history?range=6h|24h|7d` (session auth) returns CPU, memory, disk and load series,
  averaged down to ≤240 points; four Chart.js charts under the page (outside the HTMX-refreshed body so
  they are not rebuilt every 15 s). The website tour embeds a captured 24 h series
  (`window.__hostHistory`).
- Alerts: new catalog events `host_problem` ("Server problem") and `host_recovered`. A finding must hold
  for 3 consecutive readings (3 min) before it alerts, each (area, level) alerts once, escalation to
  critical alerts again, and recovery is announced once after 3 clear readings (`hostWatch`).
  Traefik not running is part of this; Docker down shows on the page but is only caught indirectly.

## Step 3 — cleanup (planned)

A "Clean up" action that prunes
build cache and unused images after showing the reclaimable size; never volumes or anything a running
app uses; dashboard-only, not exposed to agents at first.

## Notes

- Single server: only the machine DeployMate runs on.
- On a VM or container the numbers are what that machine sees (cgroup limits can differ from the host).
