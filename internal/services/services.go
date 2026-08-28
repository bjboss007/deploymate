// Package services defines the database/cache service types DeployMate can
// provision: one-click containers with generated credentials, a data volume,
// and a connection URL injected into the project's apps.
package services

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
	// ReadyCmd probes readiness from inside the container; it must exit 0
	// when the service accepts connections.
	ReadyCmd func(creds map[string]string) []string
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
