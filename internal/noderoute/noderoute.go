// Package noderoute writes Traefik file-provider routes for apps that run on worker nodes.
//
// Traefik finds ordinary apps through the local Docker socket, which cannot see a container on another machine. For an
// app pinned to a node, a small YAML file routes its domains to the node's private address and the ports its replicas
// publish there. A reconciler keeps those files matching what is running; the preview subdomains need none of this, since
// the dashboard proxies them (internal/httpserver, runtime.HostOf).
package noderoute

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/dashroute"
	"github.com/habibmuhammad/deploymate/internal/proxy"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

const filePrefix = "node-app-"

// Route is one app's route.
type Route struct {
	Slug       string
	Domains    []string
	Servers    []string // http://node-address:port, one per running replica
	HealthPath string
	Resolver   string // certresolver name; "" = Traefik's default certificate (behind a tunnel)
}

type file struct {
	HTTP struct {
		Routers  map[string]router  `yaml:"routers"`
		Services map[string]service `yaml:"services"`
	} `yaml:"http"`
}
type router struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Service     string   `yaml:"service"`
	TLS         struct {
		CertResolver string `yaml:"certResolver,omitempty"`
	} `yaml:"tls"`
}
type service struct {
	LoadBalancer struct {
		Servers     []map[string]string `yaml:"servers"`
		HealthCheck *struct {
			Path     string `yaml:"path"`
			Interval string `yaml:"interval"`
			Timeout  string `yaml:"timeout"`
		} `yaml:"healthCheck,omitempty"`
	} `yaml:"loadBalancer"`
}

// Render builds the YAML for a route. Domains must be plain DNS names: they end up inside a Traefik rule.
func Render(r Route) ([]byte, error) {
	if len(r.Domains) == 0 || len(r.Servers) == 0 {
		return nil, fmt.Errorf("a route needs at least one domain and one server")
	}
	for _, d := range r.Domains {
		if !dashroute.ValidHost(d) {
			return nil, fmt.Errorf("domain %q is not a plain lowercase DNS name", d)
		}
	}
	name := "node-" + r.Slug
	var f file
	rt := router{Rule: "Host(`" + strings.Join(r.Domains, "`, `") + "`)", EntryPoints: []string{"websecure"}, Service: name}
	rt.TLS.CertResolver = r.Resolver
	f.HTTP.Routers = map[string]router{name: rt}
	var svc service
	for _, s := range r.Servers {
		svc.LoadBalancer.Servers = append(svc.LoadBalancer.Servers, map[string]string{"url": s})
	}
	if r.HealthPath != "" {
		svc.LoadBalancer.HealthCheck = &struct {
			Path     string `yaml:"path"`
			Interval string `yaml:"interval"`
			Timeout  string `yaml:"timeout"`
		}{r.HealthPath, proxy.HealthcheckInterval, proxy.HealthcheckTimeout}
	}
	f.HTTP.Services = map[string]service{name: svc}
	out, err := yaml.Marshal(&f)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Written by DeployMate for an app on a worker node. Do not edit.\n"), out...), nil
}

// Reconciler keeps the route files in dir equal to what the apps on nodes need.
type Reconciler struct {
	St       *store.Store
	Locator  runtime.Locator
	Dir      string // Traefik's dynamic config directory
	Resolver string
	Interval time.Duration
}

// Desired computes the route of every node-pinned app that has domains and a running replica.
func (r *Reconciler) Desired() (map[string]Route, error) {
	apps, err := r.St.ListNodeApps()
	if err != nil {
		return nil, err
	}
	out := map[string]Route{}
	for _, app := range apps {
		if app.Status != "running" || app.Port <= 0 {
			continue
		}
		doms, err := r.St.ListDomains(app.ID)
		if err != nil {
			return nil, err
		}
		rows, err := r.St.ListAppReplicas(app.ID)
		if err != nil {
			return nil, err
		}
		rt := Route{Slug: app.Slug, HealthPath: appspec.HealthPath(app), Resolver: r.Resolver}
		for _, d := range doms {
			if dashroute.ValidHost(d.Hostname) {
				rt.Domains = append(rt.Domains, d.Hostname)
			}
		}
		for _, sl := range appspec.Slots(app, rows) {
			if sl.HostPort > 0 {
				rt.Servers = append(rt.Servers, fmt.Sprintf("http://%s:%d", r.Locator.HostFor(sl.Name), sl.HostPort))
			}
		}
		if len(rt.Domains) > 0 && len(rt.Servers) > 0 {
			out[app.Slug] = rt
		}
	}
	return out, nil
}

// Once writes the files that changed and removes the ones no app needs any more. It only touches its own files.
func (r *Reconciler) Once() error {
	want, err := r.Desired()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	keep := map[string]bool{}
	for slug, route := range want {
		body, err := Render(route)
		if err != nil {
			slog.Warn("noderoute: render", "app", slug, "err", err)
			continue
		}
		path := filepath.Join(r.Dir, filePrefix+slug+".yml")
		keep[filepath.Base(path)] = true
		if old, err := os.ReadFile(path); err == nil && string(old) == string(body) {
			continue
		}
		tmp, err := os.CreateTemp(r.Dir, ".node-app-*.tmp")
		if err != nil {
			return err
		}
		if _, err := tmp.Write(body); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return err
		}
		tmp.Close()
		_ = os.Chmod(tmp.Name(), 0o644)
		if err := os.Rename(tmp.Name(), path); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(r.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filePrefix) && strings.HasSuffix(e.Name(), ".yml") && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(r.Dir, e.Name()))
		}
	}
	return nil
}

// Run reconciles now and then every interval until stop is closed.
func (r *Reconciler) Run(stop <-chan struct{}) {
	t := time.NewTicker(r.Interval)
	defer t.Stop()
	for {
		if err := r.Once(); err != nil {
			slog.Error("noderoute: reconcile", "err", err)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}
