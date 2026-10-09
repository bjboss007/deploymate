package services

import (
	"fmt"
	"net/url"
)

// postgresDataDir is where the volume is mounted and where Postgres is told to keep its data.
const postgresDataDir = "/var/lib/postgresql/data"

// Postgres provisions postgres:16-alpine with a generated superuser.
var Postgres = Template{
	Type:   "postgres",
	Label:  "PostgreSQL",
	Image:  "postgres:16-alpine",
	Port:   5432,
	Mount:  postgresDataDir,
	URLEnv: "DATABASE_URL",
	CredsGen: map[string]func() string{
		"user":     func() string { return "dm" },
		"password": GenPassword,
		"db":       func() string { return "app" },
	},
	Env: func(c map[string]string) []string {
		return []string{
			"POSTGRES_USER=" + c["user"],
			"POSTGRES_PASSWORD=" + c["password"],
			"POSTGRES_DB=" + c["db"],
			// Postgres 18 moved the default data directory to /var/lib/postgresql/18/docker and
			// refuses to start when a volume is mounted at the old /var/lib/postgresql/data. Naming the
			// directory keeps every version, and every existing volume, exactly where it is.
			"PGDATA=" + postgresDataDir,
		}
	},
	ConnURL: func(c map[string]string, host string) string {
		// sslmode=disable: the managed container has no TLS and traffic stays
		// on the private docker network. Apps needing SSL can override
		// DATABASE_URL with their own env var.
		return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
			url.QueryEscape(c["user"]), url.QueryEscape(c["password"]), host, 5432, c["db"])
	},
	ReadyCmd: func(c map[string]string) []string {
		return []string{"pg_isready", "-U", c["user"], "-d", c["db"]}
	},
}

// MySQL provisions mysql:8.4 (LTS) with a generated root + app user.
var MySQL = Template{
	Type:   "mysql",
	Label:  "MySQL",
	Image:  "mysql:8.4",
	Port:   3306,
	Mount:  "/var/lib/mysql",
	URLEnv: "MYSQL_URL",
	CredsGen: map[string]func() string{
		"root_password": GenPassword,
		"user":          func() string { return "dm" },
		"password":      GenPassword,
		"db":            func() string { return "app" },
	},
	Env: func(c map[string]string) []string {
		return []string{
			"MYSQL_ROOT_PASSWORD=" + c["root_password"],
			"MYSQL_DATABASE=" + c["db"],
			"MYSQL_USER=" + c["user"],
			"MYSQL_PASSWORD=" + c["password"],
		}
	},
	ConnURL: func(c map[string]string, host string) string {
		return fmt.Sprintf("mysql://%s:%s@%s:%d/%s",
			url.QueryEscape(c["user"]), url.QueryEscape(c["password"]), host, 3306, c["db"])
	},
	ReadyCmd: func(c map[string]string) []string {
		return []string{"mysqladmin", "ping", "-h", "127.0.0.1", "-u" + c["user"], "-p" + c["password"]}
	},
}

// Redis provisions redis:7-alpine on the private network (no password —
// unreachable from outside; apps on deploymate-net only).
var Redis = Template{
	Type:   "redis",
	Label:  "Redis",
	Image:  "redis:7-alpine",
	Port:   6379,
	Mount:  "/data",
	URLEnv: "REDIS_URL",
	CredsGen: map[string]func() string{
		// Redis needs no credentials in the MVP; keep the entry for uniform
		// cred handling.
		"password": func() string { return "" },
	},
	Env: func(c map[string]string) []string {
		return nil
	},
	ConnURL: func(c map[string]string, host string) string {
		return fmt.Sprintf("redis://%s:%d/0", host, 6379)
	},
	ReadyCmd: func(c map[string]string) []string {
		return []string{"redis-cli", "ping"}
	},
}
