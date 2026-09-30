// Package appspec is the single place that turns an app row + deploy
// options into a container spec: names, labels, env, ports, and limits.
// Both deploy paths (worker git builds, handler manual deploys) and the
// start-time binding heal share it, so a spec built by one is the spec the
// other would have built — the drift the two parallel builders used to have
// is the reason this package exists.
package appspec

import (
	"strconv"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/proxy"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// NetworkName is the shared bridge all DeployMate containers live on.
const NetworkName = "deploymate-net"

// CanonicalName is the container name monitor stats, logs, and probes
// follow ("dm-" + slug).
func CanonicalName(slug string) string { return "dm-" + slug }

// StagedName is the temp container name a zero-downtime deploy starts next
// to the running one.
func StagedName(slug, deployID string) string { return "dm-" + slug + "-" + deployID }

// SlotName is the container name of replica slot n (1-based). Slot 1 is the
// canonical dm-{slug} container, so a single-replica app — and every
// container that predates replicas — needs no rename; extra slots are
// dm-{slug}-r{n}.
func SlotName(slug string, slot int) string {
	if slot <= 1 {
		return CanonicalName(slug)
	}
	return CanonicalName(slug) + "-r" + strconv.Itoa(slot)
}

// StagedSlotName is the temp name a rollout starts beside slot n.
func StagedSlotName(slug string, slot int, deployID string) string {
	if slot <= 1 {
		return StagedName(slug, deployID)
	}
	return SlotName(slug, slot) + "-" + deployID
}

// RouterName is the per-container Traefik router for slot n of a deploy.
// Unique per container: Traefik drops a router that two containers define
// differently (see internal/proxy). Slot 1 keeps the pre-replicas name.
func RouterName(slug string, slot int, deployID string) string {
	if slot <= 1 {
		return slug + "-" + deployID
	}
	return slug + "-" + deployID + "-r" + strconv.Itoa(slot)
}

// Slot is one resolved replica: its number, container name, and the loopback
// host port it publishes, plus the monitor's last verdict.
type Slot struct {
	Slot     int
	Name     string
	HostPort int
	Status   string // healthy | unhealthy | ''
	DeployID string
}

// Slots resolves an app's replicas from the replica table. An app with no
// rows (never redeployed since replicas shipped) resolves to one synthetic
// slot 1 on ResolvedPreviewPort — the pre-replicas behavior. Every reader
// (preview proxy, monitor, logs, lifecycle) goes through this.
func Slots(app store.App, rows []store.AppReplica) []Slot {
	if len(rows) == 0 {
		return []Slot{{Slot: 1, Name: CanonicalName(app.Slug), HostPort: ResolvedPreviewPort(app)}}
	}
	out := make([]Slot, 0, len(rows))
	for _, r := range rows {
		out = append(out, Slot{Slot: r.Slot, Name: r.ContainerName, HostPort: r.HostPort, Status: r.Status, DeployID: r.DeployID})
	}
	return out
}

// HealthPath returns the app's probe path, defaulting to "/".
func HealthPath(app store.App) string {
	if app.HealthPath == "" {
		return store.DefaultHealthPath
	}
	return app.HealthPath
}

// ResolvedPreviewPort returns the loopback host port the app's current
// container publishes: the stored port when a swap recorded one, else the
// deterministic per-slug hash. The preview proxy and the monitor probe
// resolve through this, so a swap's port flip is picked up automatically.
func ResolvedPreviewPort(app store.App) int {
	if app.PreviewHostPort > 0 {
		return app.PreviewHostPort
	}
	return runtime.PreviewPort(app.Slug)
}

// Labels marks containers as DeployMate-owned.
func Labels(slug string) map[string]string {
	return map[string]string{
		"deploymate.managed": "true",
		"deploymate.app":     slug,
	}
}

// Options carries everything a deploy differs on beyond the app row.
type Options struct {
	Image    string            // image reference
	Name     string            // container name (CanonicalName or StagedName)
	Port     int               // the container's listening port (0 = none)
	HostPort int               // loopback-published host port for Port (0 = no publish)
	Env      []string          // base env (service URLs, app env vars) — caller-built
	ExtraEnv map[string]string // per-deploy additions (GIT_SHA, …)
	DeployID string            // sets the deploymate.deploy label when non-empty
	Network  string            // docker network; defaults to appspec.NetworkName
	// Traefik: empty Domains = not exposed. RouterName defaults to the slug;
	// blue/green stages pass their own name + a UnixNano Priority so the
	// newest container wins the Host rule.
	Domains    []string
	RouterName string
	Priority   int64
	LEResolver string
	// Entrypoint/Cmd override the image's own values; nil = image default
	// (callers get these from the app row via SplitArgs).
	Entrypoint []string
	Cmd        []string
	// Slot is the replica number (1-based; 0 = 1). Recorded as the
	// deploymate.slot label so a container says which slot it serves.
	Slot int
}

// BuildSpec assembles the container spec for an app deploy.
func BuildSpec(app store.App, o Options) runtime.Spec {
	network := o.Network
	if network == "" {
		network = NetworkName
	}
	env := o.Env
	if o.Port > 0 {
		env = append(env, "PORT="+strconv.Itoa(o.Port))
	}
	for k, v := range o.ExtraEnv {
		env = append(env, k+"="+v)
	}

	labels := Labels(app.Slug)
	if o.DeployID != "" {
		labels["deploymate.deploy"] = o.DeployID
	}
	if o.Port > 0 {
		labels["deploymate.port"] = strconv.Itoa(o.Port)
	}
	slot := o.Slot
	if slot < 1 {
		slot = 1
	}
	labels["deploymate.slot"] = strconv.Itoa(slot)
	for k, v := range proxy.AppLabels(proxy.AppLabelsOpts{
		Slug: app.Slug, RouterName: o.RouterName, Port: o.Port, HealthPath: HealthPath(app),
		Domains: o.Domains, LEResolver: o.LEResolver, Priority: o.Priority,
	}) {
		labels[k] = v
	}

	spec := runtime.Spec{
		Name:    o.Name,
		Image:   o.Image,
		Env:     env,
		Labels:  labels,
		Network: network,
	}
	if o.Port > 0 && o.HostPort > 0 {
		spec.Port = o.Port
		spec.HostPort = o.HostPort
	}
	if app.MemLimitMB > 0 {
		spec.MemLimitMB = int64(app.MemLimitMB)
	}
	if app.CPULimit > 0 {
		spec.CPULimit = app.CPULimit
	}
	spec.Entrypoint = o.Entrypoint
	spec.Cmd = o.Cmd
	return spec
}

// SplitArgs splits a raw command string on whitespace into argv. No quoting
// support (documented contract of the deploy form): fields are simple
// whitespace-separated tokens. Empty/whitespace-only input returns nil —
// nil means "image default" downstream at docker.Create.
func SplitArgs(s string) []string {
	if len(strings.TrimSpace(s)) == 0 {
		return nil
	}
	return strings.Fields(s)
}
