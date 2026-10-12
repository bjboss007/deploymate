// Package services defines the database/cache service types DeployMate can
// provision: one-click containers with generated credentials, a data volume,
// and a connection URL injected into the project's apps.
package services

import (
	"strconv"
	"strings"
)

// Template describes how to run one service type.
type Template struct {
	Type     string
	Label    string
	Image    string
	Port     int
	Mount    string // container path the data volume is mounted at
	URLEnv   string // env key apps receive with the connection URL
	CredsGen map[string]func() string
	// Env builds the container environment from plaintext credentials.
	Env func(creds map[string]string) []string
	// ConnURL builds the connection URL from credentials and the hostname
	// apps use to reach the service (its container name).
	ConnURL func(creds map[string]string, host string) string
	// Cmd overrides the image's command from plaintext credentials; nil (or a nil
	// result) keeps the image's own.
	Cmd func(creds map[string]string) []string
	// ReadyCmd probes readiness from inside the container; it must exit 0
	// when the service accepts connections.
	ReadyCmd func(creds map[string]string) []string
}

// Components are the connection details as separate variables, for apps and frameworks that
// take host, port and password apart instead of one URL (Spring, Django settings, many
// Redis clients). The names follow the URL variable: DATABASE_URL gives DATABASE_HOST,
// DATABASE_PORT, DATABASE_USER, DATABASE_PASSWORD and DATABASE_NAME; REDIS_URL gives
// REDIS_HOST, REDIS_PORT and, when the service has one, REDIS_PASSWORD. An app's own
// variables of the same name win. host is how apps reach the service (its container name).
func (t Template) Components(creds map[string]string, host string) map[string]string {
	prefix := strings.TrimSuffix(t.URLEnv, "_URL")
	out := map[string]string{
		prefix + "_HOST": host,
		prefix + "_PORT": strconv.Itoa(t.Port),
	}
	switch t.Type {
	case "postgres", "mysql":
		out[prefix+"_USER"] = creds["user"]
		out[prefix+"_PASSWORD"] = creds["password"]
		out[prefix+"_NAME"] = creds["db"]
	case "redis":
		if creds["password"] != "" {
			out[prefix+"_PASSWORD"] = creds["password"]
		}
	}
	return out
}

// All lists the provisionable service types.
var All = []Template{Postgres, MySQL, Redis}

// ForType returns the template for a service type.
func ForType(t string) (Template, bool) {
	for _, tpl := range All {
		if tpl.Type == t {
			return tpl, true
		}
	}
	return Template{}, false
}

// VolumeName returns the named volume for a service (kept even after the
// service is deleted — data deletion is an explicit act).
func VolumeName(slug string) string { return "dm-svc-" + slug + "-data" }
