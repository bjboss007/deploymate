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

### 1. Remote builds
Point the BuildKit step at a stronger machine (`DEPLOYMATE_BUILD_HOST`, an SSH or TCP BuildKit endpoint). Only the build
moves; the result still has to reach the machine that runs it: stream it back (`docker save | docker load`, slow over a
WAN for multi-GB ML images) or push it to a small private registry on the server. For large images, phase 2 is better:
build **and** run on the powerful node, so nothing is shipped.

### 2. Nodes and placement
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

## Open questions for the owner
1. The friend's hardware: NVIDIA GPU on Linux or Windows (WSL), or a Mac? What VRAM?
2. Network: both machines behind home routers? Is Tailscale or WireGuard acceptable as the link?
3. First workload: a model server (needs a stable URL), batch video encoding, or training runs?
4. Is the friend's machine also their daily computer (so it needs availability windows and low priority)?
5. Is this only for the owner and trusted friends, or is a wider pool the goal? The second multiplies the security work.
