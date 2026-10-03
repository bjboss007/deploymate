// Package usage answers "which apps actually hold a connection to this
// database or cache right now?". Every app in a project and environment is
// GIVEN the service's connection URL; whether it USES it is a different fact,
// and this is how the dashboard tells them apart: it asks the service itself
// who is connected and matches those client addresses to app containers.
package usage

import (
	"context"
	"regexp"
	"strings"
)

// Execer is the part of the runtime the probes need.
type Execer interface {
	Exec(ctx context.Context, name string, cmd []string) (string, error)
	ExecEnv(ctx context.Context, name string, cmd, env []string) (string, error)
}

// Clients returns the set of client IP addresses currently connected to a
// service container. ok is false when the service type is not probed or the
// probe failed — "unknown", never "nobody".
func Clients(ctx context.Context, rt Execer, svcType, container string, creds map[string]string) (ips map[string]bool, ok bool) {
	switch svcType {
	case "postgres":
		user := creds["user"]
		if user == "" {
			user = "postgres"
		}
		out, err := rt.Exec(ctx, container, []string{"psql", "-U", user, "-d", "postgres", "-At", "-c",
			"select client_addr::text from pg_stat_activity where client_addr is not null"})
		if err != nil {
			return nil, false
		}
		return ParsePostgres(out), true
	case "redis":
		out, err := rt.Exec(ctx, container, []string{"redis-cli", "client", "list"})
		if err != nil {
			return nil, false
		}
		return ParseRedis(out), true
	case "mysql":
		out, err := rt.ExecEnv(ctx, container,
			[]string{"mysql", "-uroot", "-N", "-B", "-e", "select host from information_schema.processlist"},
			[]string{"MYSQL_PWD=" + creds["root_password"]})
		if err != nil {
			return nil, false
		}
		return ParseMySQL(out), true
	}
	return nil, false
}

var ipRe = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)

func add(set map[string]bool, s string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "/"); i >= 0 { // pg inet text "10.0.0.5/32"
		s = s[:i]
	}
	if ipRe.MatchString(s) {
		set[s] = true
	}
}

// ParsePostgres reads `select client_addr::text …` output (one address per row).
func ParsePostgres(out string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		add(set, l)
	}
	return set
}

var redisAddr = regexp.MustCompile(`(?:^|\s)addr=([0-9.]+):\d+`)

// ParseRedis reads `CLIENT LIST` output (one connection per line, addr=ip:port).
func ParseRedis(out string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		if m := redisAddr.FindStringSubmatch(l); m != nil {
			add(set, m[1])
		}
	}
	return set
}

// ParseMySQL reads `select host from information_schema.processlist` output
// (host is "ip:port", or a bare name for local/system threads).
func ParseMySQL(out string) map[string]bool {
	set := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if i := strings.LastIndex(l, ":"); i >= 0 {
			l = l[:i]
		}
		add(set, l)
	}
	return set
}
