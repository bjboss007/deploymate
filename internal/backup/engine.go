package backup

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/habibmuhammad/deploymate/internal/runtime"
)

// engine is what differs between database types: how to read the data out and how to put it back.
// Everything else (key, encryption, upload, retention, scheduling) is shared.
type engine interface {
	// check validates the stored credentials before anything runs.
	check(creds map[string]string) error
	// dump returns the whole database as bytes, running its tools inside the container.
	dump(ctx context.Context, rt runtime.Runtime, name string, creds map[string]string) ([]byte, error)
	// restore replaces the database's contents with data. Callers have checked the container runs.
	restore(ctx context.Context, rt runtime.Runtime, name string, creds map[string]string, data []byte) error
}

// stepError names the step that failed, for the event text ("backup of X failed (pg_dump): …").
type stepError struct {
	step string
	err  error
}

func (e stepError) Error() string { return e.err.Error() }
func (e stepError) Unwrap() error { return e.err }

func stepFail(step string, err error, out string) error {
	if out != "" {
		err = fmt.Errorf("%w: %s", err, out)
	}
	return stepError{step, err}
}

func engineFor(serviceType string) (engine, bool) {
	switch serviceType {
	case "postgres":
		return postgresEngine{}, true
	case "mysql":
		return mysqlEngine{}, true
	case "redis":
		return redisEngine{}, true
	case "mongodb":
		return mongoEngine{}, true
	}
	return nil, false
}

// identRe guards the db/user identifiers interpolated into SQL and tool arguments. They come from
// our own credential templates ("app"/"dm"), but a corrupted credential must fail loudly, never
// become a command.
var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func needIdents(creds map[string]string, keys ...string) error {
	for _, k := range keys {
		if !identRe.MatchString(creds[k]) {
			return errors.New("service has no usable " + k + " credential")
		}
	}
	return nil
}

// runSteps runs commands in order, naming the first command in the failure.
func runSteps(ctx context.Context, rt runtime.Runtime, name string, env []string, steps [][]string) error {
	for _, step := range steps {
		if out, err := rt.ExecEnv(ctx, name, step, env); err != nil {
			return stepFail("step "+step[0], err, out)
		}
	}
	return nil
}

// ---- PostgreSQL ----------------------------------------------------------------

type postgresEngine struct{}

func (postgresEngine) check(c map[string]string) error { return needIdents(c, "user", "db") }

func (postgresEngine) dump(ctx context.Context, rt runtime.Runtime, name string, c map[string]string) ([]byte, error) {
	out, err := rt.ExecEnv(ctx, name, []string{"pg_dump", "-Fc", "-U", c["user"], "-d", c["db"], "-f", containerDump}, pgEnv(c))
	if err != nil {
		return nil, stepFail("pg_dump", err, out)
	}
	return readAndClean(ctx, rt, name)
}

func (postgresEngine) restore(ctx context.Context, rt runtime.Runtime, name string, c map[string]string, data []byte) error {
	user, db := c["user"], c["db"]
	if err := rt.WriteFile(ctx, name, containerRestore, data); err != nil {
		return stepFail("copy dump in", err, "")
	}
	return runSteps(ctx, rt, name, pgEnv(c), [][]string{
		// Terminate app-DB connections first — drop fails on live clients.
		{"psql", "-U", user, "-d", "postgres", "-c",
			fmt.Sprintf("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()", db)},
		{"psql", "-U", user, "-d", "postgres", "-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s", db)},
		{"psql", "-U", user, "-d", "postgres", "-c", fmt.Sprintf("CREATE DATABASE %s OWNER %s", db, user)},
		{"pg_restore", "-Fc", "-U", user, "-d", db, "--no-owner", containerRestore},
		{"rm", "-f", containerRestore},
	})
}

// readAndClean copies the dump file out and removes it from the container either way.
func readAndClean(ctx context.Context, rt runtime.Runtime, name string) ([]byte, error) {
	defer func() { _, _ = rt.Exec(ctx, name, []string{"rm", "-f", containerDump}) }()
	data, err := rt.ReadFile(ctx, name, containerDump)
	if err != nil {
		return nil, stepFail("read dump", err, "")
	}
	return data, nil
}

// ---- MySQL ---------------------------------------------------------------------

type mysqlEngine struct{}

func mysqlEnv(c map[string]string) []string { return []string{"MYSQL_PWD=" + c["root_password"]} }

func (mysqlEngine) check(c map[string]string) error {
	if c["root_password"] == "" {
		return errors.New("service has no stored root password")
	}
	return needIdents(c, "db")
}

// Dumps run as root: --routines/--events need more rights than the app user has. The dump is a
// consistent snapshot (--single-transaction) of InnoDB tables without locking them.
func (mysqlEngine) dump(ctx context.Context, rt runtime.Runtime, name string, c map[string]string) ([]byte, error) {
	out, err := rt.ExecEnv(ctx, name, []string{"mysqldump", "-uroot", "--single-transaction", "--routines", "--triggers", "--events",
		"--default-character-set=utf8mb4", "--result-file=" + containerDump, c["db"]}, mysqlEnv(c))
	if err != nil {
		return nil, stepFail("mysqldump", err, out)
	}
	return readAndClean(ctx, rt, name)
}

func (mysqlEngine) restore(ctx context.Context, rt runtime.Runtime, name string, c map[string]string, data []byte) error {
	db := c["db"]
	if err := rt.WriteFile(ctx, name, containerRestore, data); err != nil {
		return stepFail("copy dump in", err, "")
	}
	// The app user's grants are stored by database name, so they survive the drop.
	return runSteps(ctx, rt, name, mysqlEnv(c), [][]string{
		{"mysql", "-uroot", "-e", fmt.Sprintf("DROP DATABASE IF EXISTS `%s`; CREATE DATABASE `%s` CHARACTER SET utf8mb4", db, db)},
		{"sh", "-c", fmt.Sprintf("mysql -uroot --default-character-set=utf8mb4 %s < %s", db, containerRestore)},
		{"rm", "-f", containerRestore},
	})
}

// ---- Redis ---------------------------------------------------------------------

type redisEngine struct{}

func redisEnv(c map[string]string) []string {
	if pw := c["password"]; pw != "" {
		return []string{"REDISCLI_AUTH=" + pw} // keeps the password off the command line
	}
	return nil
}

func (redisEngine) check(map[string]string) error { return nil }

func (redisEngine) dump(ctx context.Context, rt runtime.Runtime, name string, c map[string]string) ([]byte, error) {
	out, err := rt.ExecEnv(ctx, name, []string{"redis-cli", "--rdb", containerDump}, redisEnv(c))
	if err != nil {
		return nil, stepFail("redis-cli --rdb", err, out)
	}
	return readAndClean(ctx, rt, name)
}

// redisData is where the official image keeps dump.rdb.
const redisData = "/data/dump.rdb"

// Redis loads its snapshot only at start, and writes one again at shutdown, so a live server would
// overwrite a restored file. The container is stopped (that last write happens now), the file is put
// in place, and the container started: a few seconds of downtime.
func (redisEngine) restore(ctx context.Context, rt runtime.Runtime, name string, c map[string]string, data []byte) error {
	if err := rt.Stop(ctx, name, 30); err != nil {
		return stepFail("stop", err, "")
	}
	if err := rt.WriteFile(ctx, name, redisData, data); err != nil {
		_ = rt.Start(ctx, name) // never leave the cache down because the copy failed
		return stepFail("copy dump in", err, "")
	}
	if err := rt.Start(ctx, name); err != nil {
		return stepFail("start", err, "")
	}
	return nil
}

// ---- MongoDB -------------------------------------------------------------------

type mongoEngine struct{}

func mongoURI(c map[string]string) string {
	return fmt.Sprintf("mongodb://%s:%s@127.0.0.1:27017/?authSource=admin", url.QueryEscape(c["user"]), url.QueryEscape(c["password"]))
}

func (mongoEngine) check(c map[string]string) error {
	if c["password"] == "" {
		return errors.New("service has no stored password")
	}
	return needIdents(c, "user", "db")
}

func (mongoEngine) dump(ctx context.Context, rt runtime.Runtime, name string, c map[string]string) ([]byte, error) {
	out, err := rt.Exec(ctx, name, []string{"mongodump", "--uri=" + mongoURI(c), "--db=" + c["db"], "--archive=" + containerDump})
	if err != nil {
		return nil, stepFail("mongodump", err, out)
	}
	return readAndClean(ctx, rt, name)
}

func (mongoEngine) restore(ctx context.Context, rt runtime.Runtime, name string, c map[string]string, data []byte) error {
	if err := rt.WriteFile(ctx, name, containerRestore, data); err != nil {
		return stepFail("copy dump in", err, "")
	}
	return runSteps(ctx, rt, name, nil, [][]string{
		{"mongorestore", "--uri=" + mongoURI(c), "--archive=" + containerRestore, "--drop", "--nsInclude=" + c["db"] + ".*"},
		{"rm", "-f", containerRestore},
	})
}
