// Package dashroute writes the Traefik file-provider config that routes the
// dashboard's own domain (and the optional preview subdomains) to DeployMate.
//
// The dashboard is a host systemd service listening on loopback, not a
// container, so Traefik's docker provider never sees it. Traefik runs on the
// host network (deploy/traefik-run.sh) and reaches it at 127.0.0.1.
package dashroute

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// FileName is the dynamic-config file inside the Traefik dynamic directory.
const FileName = "dashboard.yml"

// Opts describes the routes.
type Opts struct {
	Host        string // dashboard hostname, e.g. dm.example.com ("" = no route)
	PreviewHost string // optional: {slug}.PreviewHost goes to the dashboard's preview handler
	ListenAddr  string // the dashboard's DEPLOYMATE_ADDR
	Resolver    string // Traefik certresolver ("" = default certificate, e.g. behind a tunnel)
}

var hostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// ValidHost reports whether s is a plain lowercase DNS name. Anything else is
// refused, because the name ends up inside a Traefik rule.
func ValidHost(s string) bool { return len(s) <= 253 && hostRE.MatchString(s) }

// Upstream turns the dashboard's listen address into the URL Traefik (on the
// host network) should call. A wildcard bind still answers on loopback.
func Upstream(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("dashboard address %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

type tlsBlock struct {
	CertResolver string `yaml:"certResolver,omitempty"`
}

type router struct {
	Rule        string   `yaml:"rule"`
	EntryPoints []string `yaml:"entryPoints"`
	Service     string   `yaml:"service"`
	TLS         tlsBlock `yaml:"tls"`
	Priority    int      `yaml:"priority,omitempty"`
}

type server struct {
	URL string `yaml:"url"`
}

type service struct {
	LoadBalancer struct {
		Servers []server `yaml:"servers"`
	} `yaml:"loadBalancer"`
}

type file struct {
	HTTP struct {
		Routers  map[string]router  `yaml:"routers"`
		Services map[string]service `yaml:"services"`
	} `yaml:"http"`
}

// Render builds the YAML for the options. It errors on an invalid hostname.
func Render(o Opts) ([]byte, error) {
	if !ValidHost(o.Host) {
		return nil, fmt.Errorf("dashboard host %q is not a plain lowercase DNS name", o.Host)
	}
	up, err := Upstream(o.ListenAddr)
	if err != nil {
		return nil, err
	}
	var f file
	f.HTTP.Routers = map[string]router{
		"deploymate-dashboard": {
			Rule: "Host(`" + o.Host + "`)", EntryPoints: []string{"websecure"},
			Service: "deploymate-dashboard", TLS: tlsBlock{CertResolver: o.Resolver},
		},
	}
	var svc service
	svc.LoadBalancer.Servers = []server{{URL: up}}
	f.HTTP.Services = map[string]service{"deploymate-dashboard": svc}

	if o.PreviewHost != "" {
		if !ValidHost(o.PreviewHost) {
			return nil, fmt.Errorf("preview host %q is not a plain lowercase DNS name", o.PreviewHost)
		}
		// Priority 1: an app's own exact Host() router must always win over this
		// catch-all (Traefik's default priority is the rule's length, which the
		// regexp would win). Certificates cannot be requested for a pattern, so
		// previews use the default certificate unless something in front serves one.
		f.HTTP.Routers["deploymate-previews"] = router{
			Rule:        "HostRegexp(`^[a-z0-9-]+\\." + regexp.QuoteMeta(o.PreviewHost) + "$`)",
			EntryPoints: []string{"websecure"}, Service: "deploymate-dashboard", TLS: tlsBlock{}, Priority: 1,
		}
	}
	out, err := yaml.Marshal(&f)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Written by DeployMate at startup from DEPLOYMATE_DASHBOARD_HOST. Do not edit.\n"), out...), nil
}

// Write puts the config in dir (Traefik's dynamic directory), replacing it
// atomically. With no Host it removes the file, so unsetting the variable and
// restarting takes the route away. It only touches its own file.
func Write(dir string, o Opts) error {
	path := filepath.Join(dir, FileName)
	if o.Host == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	body, err := Render(o)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".dashboard-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil { // Traefik's container must be able to read it
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
