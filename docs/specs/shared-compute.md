# Shared compute (worker nodes, remote builds, GPU jobs)

Proposed 2026-10-20 after the owner described the need: a small server running DeployMate, a friend with a much more
powerful machine, and heavy work (ML and AI models, video) that should run on whatever machine can take it. Nothing here
is built. The owner agreed to the staged plan below.

## What "sharing compute" can and cannot mean
- **Run things on another machine.** DeployMate on the small server decides; the powerful machine runs the containers.
- **Spread many pieces of work over machines.** Video is the clean case: cut into segments, encode each, join. Model
  evaluation sweeps and batch inference are the same shape.
- **One program using two machines' power at once.** A platform cannot do this to an arbitrary program. It works for
  software built for it (llama.cpp's RPC mode, vLLM with Ray, exo), and only over a fast LAN: splitting one model over the
  public internet is slower than running it on the stronger machine alone. DeployMate can deploy and wire those, not
  implement them.

## Hardware facts that shape the design
- **NVIDIA on Linux** is the supported GPU path (NVIDIA Container Toolkit; Docker `--gpus`).
- **Docker on a Mac cannot use the Apple GPU.** A Mac node runs CPU work in containers only; Metal workloads need a native
  runner, which is out of scope for now.
- Model weights and media are large. They belong in a per-node cache or object storage, never in the database or pushed
  through the dashboard.

## Phases (in order)

### 1. Remote builds (built 2026-10-26)
Point the BuildKit step at a stronger machine (`DEPLOYMATE_BUILD_HOST`, an SSH or TCP BuildKit endpoint). Only the build
moves; the result still has to reach the machine that runs it: stream it back (`docker save | docker load`, slow over a
WAN for multi-GB ML images) or push it to a small private registry on the server. For large images, phase 2 is better:
build **and** run on the powerful node, so nothing is shipped.

*As built:* `DEPLOYMATE_BUILD_HOST=tcp://<tailscale-ip>:1234` (`internal/builder/remote.go`). Railpack builds get it as
`BUILDKIT_HOST`; Dockerfile builds use a buildx builder `dm-remote` (driver `remote`, created or re-pointed before the
build). Only `tcp://host:port` is accepted (the value ends up in a command line). **An unreachable host never fails a
deploy:** the build log says so and the build runs on this server. The log also says `building on <host>`. Source and
build context are sent to the build machine; the finished image is streamed back and loaded locally; the build cache
stays on the build machine, so repeat builds are fast. Verified on real Docker with a second BuildKit over TCP: a Dockerfile
build and a Railpack static-site build (`staticfile`) both ran on the remote daemon, the images were loaded locally, and the remote
cache changed (`DEPLOYMATE_DOCKER_TESTS=1 go test ./internal/builder -run RemoteBuildKit`, about 4 minutes, needs several GB of Docker disk). *Security:* the BuildKit daemon runs privileged and, over TCP, unauthenticated: anyone who can reach the
port can run code as root on the build machine. Bind it to the Tailscale address only and restrict who may reach port 1234
with a Tailscale ACL (the DeployMate server only).

### 2. Nodes and placement
**Step 2a, built 2026-10-26: nodes exist and can be reached.** Migration `0026_nodes`; `Nodes` page (sidebar, under Server): create this
server's SSH key (`<data>/keys/node_ed25519`, shown as a public line), add a node (`ssh://user@host[:port]` or `tcp://host:port`,
strictly validated so a value can never be read by ssh as an option), Check now, Drain/Resume, Remove. `runtime.NewRemote` speaks Docker
over `ssh user@host docker system dial-stdio` (our own stdio `net.Conn`, no new dependency; `-o BatchMode=yes`, host key
accept-new, the node's ssh error text surfaces in the page) and `Docker.Capacity` reports CPUs, RAM, OS, Docker version and whether
the NVIDIA runtime is installed. `nodes.Watcher` re-checks every minute; a node needs three failed checks in a row to show offline;
the owner's Drain survives successful checks. Tested with unit tests, and the SSH transport against real Docker with a stand-in
`ssh` (`DEPLOYMATE_SSH`); **not yet tried over a real SSH connection to a second machine.** Nothing is placed on nodes yet.

**Step 2b, built 2026-10-26: a project can run on a node.** As built:
- **Whole projects are pinned** (`projects.node_id`, set on the project page, only while it has no services and nothing was ever
  deployed; a node with a project on it cannot be removed). Apps and their databases then share the node's own `deploymate-net`,
  so the existing connection URLs keep working. Cross-node service access is not planned.
- **`nodes.Router`** is a `runtime.Runtime` that sends each call to the machine the container lives on. A new container's
  node comes from its `deploymate.app` / `deploymate.service` label; the placement is remembered by container name
  (`container_nodes`, survives restarts; a swap's rename moves it). No caller changed except `main`, which hands everyone the
  router. A draining or offline node takes **no new containers, including redeploys of what already runs there**, and the
  work never falls back to this server.
- **Images** are copied from this server's daemon to the node (`docker save | docker load` over the same ssh transport) when the
  node lacks them, and pulled by the node when this server lacks them too. Every deploy of a new tag copies the whole image:
  slow for multi-GB images over a home link; building on the node is the later optimisation.
- **Ports** an app publishes bind to the node's private-network address (the endpoint's host), never its public one.
  `runtime.HostOf` gives the address to reach a container at; the health probe, the swap probe and the preview proxy use it.
  The staged container's port is chosen free on **this** server, so on the node it can collide with another process (the deploy
  then fails with Docker's message).
- **Traefik:** `internal/noderoute` reconciles one file route per running app with domains (every 15 s) to
  `node-address:port` per replica, with the same health check; stale files are removed. The preview subdomains and `/preview/`
  go through the dashboard proxy and need no route.
- **Architecture:** an image built for one CPU is never copied to a node with another (`ImageArch` / `HostArch`): the node pulls its
  own, which works for public multi-architecture images; for an image only this server has, the error says "built for arm64, the node runs
  amd64". Found by trying the owner's real server (x86_64) from an arm64 Mac.
- **Real-server finding (2026-10-26):** over the owner's Cloudflare SSH path the node check reaches the server in about 2.5 s, but the Docker
  client replaced ssh's own error with "Is the docker daemon running?". `NewRemote` now returns ssh's message plus a hint (not in
  `authorized_keys`, not in the `docker` group, host key changed, Tailscale down). The SSH user on the server (`habib-muhammad`) is not in
  the `docker` group, so the end-to-end placement test there needs that changed first. A reusable test exists:
  `DEPLOYMATE_REAL_NODE=ssh://user@host [DEPLOYMATE_REAL_NODE_KEY=…] go test ./internal/nodes -run RealSecondMachine -v`
  (places an alpine container on the machine, runs `uname -m`, removes it; no network left behind).
  **Passed on the owner's server (2026-10-26)** after the SSH user was added to the docker group: node reported 4 CPUs, 3.6 GB,
  Ubuntu 24.04.5, x86_64, Docker 29.1.3; the container (created from an arm64 Mac) ran there (`uname -m` = x86_64) and was removed, leaving
  no container, image or network. That run also showed that a node endpoint may be an SSH config alias that is not a DNS name:
  the node's address is now resolved only when a published port needs it (otherwise the error says to use the Tailscale IP).
- **Private address:** a node can carry an optional `address` (migration 0028) at which its published ports are bound and reached,
  used when the SSH endpoint is an alias or goes through a tunnel (the owner's server: ssh through Cloudflare, ports on the LAN).
- **Verified end to end on two real machines (2026-10-26):** this Mac ran DeployMate (LE mode off) and the owner's server was the node
  (ssh through Cloudflare, `address` = its LAN IP). Through the dashboard's own forms: node added and ready; project pinned; Redis started
  (container on the node, image pulled there, nothing on the Mac); an `nginx:alpine` app deployed in 16 s (the node pulled its own amd64 image
  because the Mac's is arm64); it answered over the LAN at `node-ip:port` and through `/preview/`; its container resolved the Redis by name on
  the node's `deploymate-net`; after moving the app to the Redis's environment and redeploying, `REDIS_URL/HOST/PORT/PASSWORD` were injected and
  the Redis was reachable; the swap replaced the container on a new port with the old one removed; a domain produced the Traefik file route
  (current port, health check) and a real Traefik container serving it returned the app for that Host and 404 for another; hardening flags
  were applied on the node. The server was returned to its exact earlier state (containers, networks, volumes, images identical).
  Findings: (a) **deleting a project never removed its containers**, on a single server too: fixed (`removeAppContainers` /
  `removeServiceContainer`, project delete names any it could not remove); (b) apps only receive services of their own environment, and a
  new service omitted-environment defaulted to production while a new app defaulted to dev (the form always sends one, so only the API/MCP
  tripped; **fixed 2026-10-26**: an omitted environment now joins the one the project already uses, else dev, and the project page explains a
  database that sits in another environment than every app). Not re-run since: the throwaway
  script (it used `curl` against the forms and `docker` over ssh); the steps above are the procedure.
- **Tested:** the router with fake runtimes (placement, image copy/pull, drain/offline, rename, restart, unreachable node);
  routes (render, reconcile, only own files); and a **real second Docker daemon** (Docker-in-Docker) as the node: the container
  is created there, the image copied, it runs there, `exec`/`stats`/`inspect` work through the router, and this machine's
  daemon never sees it (`DEPLOYMATE_DOCKER_TESTS=1`). **Not tried: a real two-machine deploy end to end** (SSH key
  authentication, the node's private address, Traefik reaching it), which one Mac cannot reproduce.

What reading the code showed, and the design it forced:
- Every consumer takes one `runtime.Runtime`, and its methods address containers by *name*, so a `Router` Runtime that resolves
  name → node (from a `node_id` on apps and services, default local) fits without changing callers. Image methods (`HasImage`,
  `PullImage`, `Create` with a built tag) must go to the node that runs the container.
- **Images:** the build must land in the node's daemon. Railpack and `docker buildx --load` both honour `DOCKER_HOST`, so building
  for a node means pointing them at it (over the same ssh transport) and, ideally, using that node's BuildKit.
- **Routing:** Traefik discovers containers through the local Docker socket, so a container on a node is invisible to it. Routes
  for node apps are written as file-provider entries to `node-address:port` (the mechanism `internal/dashroute` already uses),
  which needs the app's port published on the node's private address.
- **Networking:** apps reach services by container name on `deploymate-net`, which is per machine. **A project environment is
  therefore pinned to one node: its apps and services run together** (cross-node service access is not planned). Volumes are
  node-local for the same reason.
- Monitoring, logs, metrics, exec and backups already go through `Runtime`, so they follow the router.
- Decided: whole projects (see above).
- A **node** is a machine with an address on a private network (WireGuard or Tailscale; a tunnel works for HTTP only),
  Docker, and, optionally, GPUs. Table `nodes`: name, address, token hash, labels, capacity (CPUs, RAM, GPUs with model and
  VRAM), state (`ready`, `draining`, `offline`), last seen.
- Connection, in order of effort: (a) Docker over SSH on the private network, using the existing `Runtime` seam with a
  different client; (b) an outbound **worker agent** (`deploymate worker`) that dials the control server, for nodes behind
  NAT such as a friend's home PC. The agent is the long-term answer; (a) proves the model.
- An app or service gets `node` (pinned) or `requires` (`gpu: 1`, `vram: 16g`, a label). The scheduler picks a ready node
  with free capacity. **Volumes are node-local**, so a service with data is pinned to its node; DeployMate says so.
- Routing: Traefik reaches the app at `node-address:port` over the private network. Logs, metrics and health come from the
  node's runtime like any local container.
- **Availability windows** matter for a friend's machine: `draining` stops new work, and a node can be set to take jobs
  only at certain hours or only when idle.

### 3. Jobs
A new run-to-completion object, next to apps and services: image, command, resources (CPU, RAM, GPUs), timeout, retries,
and where inputs and outputs live. Inputs and outputs go through the object-storage destinations that backups already
use; large files never pass through the dashboard. **Array jobs** (`parallel: N`, with `JOB_INDEX` and `JOB_COUNT`
injected) cover video segments and parameter sweeps without a workflow engine; the user's own container does the split and
the join. Status, logs, exit code and duration appear on a Jobs page; a failed job can retry.

### 4. Recipes (docs, not features)
Worked examples: Ollama or vLLM serving a model on the GPU node behind a stable URL; FFmpeg segment encoding as an array
job; llama.cpp RPC across two LAN machines using the peer addresses DeployMate injects.

## Security and trust
- **A node can read everything that runs on it, including environment variables and database contents.** Nodes must be
  machines whose owner you trust. The dashboard must say this when a node is added and send a node only the secrets of what
  runs there (no fleet-wide secrets).
- Each node has its own token, shown once, revocable; the private network carries all control traffic; nothing is exposed
  to the internet except through Traefik as today.
- **Untrusted code on a donated machine is a different project** (strong isolation, no host access, abuse handling).
  Not planned. A "marketplace" of strangers' GPUs is out of scope.
- Resource limits apply on nodes as they do locally (memory, CPUs, pids); GPU memory is not isolated between containers.

## Not planned
Splitting a single arbitrary process across machines; live migration of containers between nodes; shared filesystems
across nodes; Apple GPU in containers.

## Verification
Unit tests with a fake node runtime (placement by labels, VRAM and free capacity, draining, offline, pinned services).
Real tests: two machines or a VM on a private network; a GPU node for the `--gpus` path (CI cannot cover it); an array job
whose outputs land in object storage; a node going offline mid-job.

## Decisions (owner, 2026-10-26)
1. **Hardware and workload are open ("any of them").** Nodes therefore declare what they have (CPUs, RAM, optional GPUs); nothing is
   assumed to be NVIDIA, and GPU support is a capability, not a requirement.
2. **Link: Tailscale, probably.** That removes the NAT problem, so phase 2 starts with Docker over SSH on the tailnet and no agent.
   The outbound worker agent is only needed for machines that cannot join a tailnet.
3. **Availability is communicated in advance.** Nodes carry an availability window (hours or a "paused until" time), set by the node's
   owner, which the scheduler respects; `draining` for the immediate case. No automatic idle detection.
4. **Trusted friends only.** The trust model stands: a node sees everything that runs on it. No untrusted-code isolation work.

First step is phase 1 (remote builds), because it needs only a reachable BuildKit and no scheduler.

## GPU placement, first step (built 2026-10-27)
Per-app switch `apps.gpu` ("Use the machine's GPUs", dashboard only; `gpu` is shown in the API app output): the
container gets `--gpus all` on whichever machine its project runs. `runtime.Docker.Create` refuses with a plain reason
when the machine has no NVIDIA runtime (Docker Desktop gets its own message), and the switch is refused up front when
the project's node has none. Nodes record their cards (`nodes.gpu_info`, from `nvidia-smi` over the node's SSH login,
shown on the Nodes page and as `gpus` in `list_nodes`). Placement stays manual: the owner pins the project to the GPU
node. Verified: unit tests, and the refusal against this Mac's real Docker. **Not verified on a real NVIDIA machine.**
Not built: automatic node choice by free GPU/VRAM, a GPU count, GPU in `configure_app`, jobs.

### Follow-up (same day): persistent folders for apps
`apps.volumes` (migration 0030): absolute paths, one per line, each a named volume `dm-app-<slug>-<crc32 of path>`
mounted by `appspec.BuildSpec`; the volume lives on whichever machine the container runs, survives deploys and app
deletion (like a service's volume), and is shared by replicas. Validation in `appspec.ParseVolumes` (absolute, no `..`,
no reserved paths, at most 8). Dashboard form on the app page; `volumes` and `gpu` accepted by `configure_app` and shown
in the API app output. Not built: backups of these volumes, removing orphaned volumes, per-replica volumes.
