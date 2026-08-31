// Package appspec is the single place that turns an app row + deploy
// options into a container spec: names, labels, env, ports, and limits.
// Both deploy paths (worker git builds, handler manual deploys) and the
// start-time binding heal share it, so a spec built by one is the spec the
// other would have built — the drift the two parallel builders used to have
// is the reason this package exists.
package appspec

import (
	"strconv"

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
	for k, v := range proxy.AppLabels(proxy.AppLabelsOpts{
		Slug: app.Slug, RouterName: o.RouterName, Port: o.Port,
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
	return spec
}
