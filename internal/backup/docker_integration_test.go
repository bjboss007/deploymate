package backup

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
)

// Real-Docker round trip for every database engine: seed rows, dump, wipe, restore, count the rows.
// It pulls images and takes a few minutes, so it only runs with DEPLOYMATE_DOCKER_TESTS=1.
func TestEnginesRoundTripOnRealDocker(t *testing.T) {
	if os.Getenv("DEPLOYMATE_DOCKER_TESTS") != "1" {
		t.Skip("set DEPLOYMATE_DOCKER_TESTS=1 to run against a real Docker daemon")
	}
	rt, err := runtime.NewDocker()
	if err != nil {
		t.Skip("no docker: ", err)
	}
	type tc struct {
		tpl            services.Template
		seed, wipe     []string // commands, run through ExecEnv with env
		count          []string
		env            []string
		want           string
		seedSh, wipeSh string
	}
	cases := map[string]func(c map[string]string) tc{
		"postgres": func(c map[string]string) tc {
			psql := func(q string) []string { return []string{"psql", "-U", c["user"], "-d", c["db"], "-At", "-c", q} }
			return tc{tpl: services.Postgres, env: pgEnv(c), want: "3",
				seed: psql("create table t(x int); insert into t values (1),(2),(3)"), wipe: psql("drop table t"), count: psql("select count(*) from t")}
		},
		"mysql": func(c map[string]string) tc {
			my := func(q string) []string { return []string{"mysql", "-uroot", "-N", "-B", "-e", q} }
			return tc{tpl: services.MySQL, env: mysqlEnv(c), want: "3",
				seed: my("create table app.t(x int); insert into app.t values (1),(2),(3)"), wipe: my("drop table app.t"), count: my("select count(*) from app.t")}
		},
		"redis": func(c map[string]string) tc {
			r := func(a ...string) []string { return append([]string{"redis-cli"}, a...) }
			return tc{tpl: services.Redis, env: redisEnv(c), want: "3",
				seed: r("mset", "a", "1", "b", "2", "c", "3"), wipe: r("flushall"), count: r("dbsize")}
		},
		"mongodb": func(c map[string]string) tc {
			sh := func(js string) []string {
				return []string{"mongosh", "--quiet", "-u", c["user"], "-p", c["password"], "--authenticationDatabase", "admin", "--eval", js}
			}
			return tc{tpl: services.MongoDB, want: "3",
				seed: sh("db.getSiblingDB('app').t.insertMany([{x:1},{x:2},{x:3}]).insertedIds.length"),
				wipe: sh("db.getSiblingDB('app').dropDatabase().ok"), count: sh("db.getSiblingDB('app').t.countDocuments()")}
		},
	}
	for name, mk := range cases {
		name, mk := name, mk
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			probe := mk(nil)
			creds := map[string]string{}
			for k, gen := range probe.tpl.CredsGen {
				creds[k] = gen()
			}
			c := mk(creds)
			cname := "dm-itest-" + name
			_ = rt.Remove(ctx, cname)
			if ok, _ := rt.HasImage(ctx, c.tpl.Image); !ok {
				if err := rt.PullImage(ctx, c.tpl.Image); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := rt.Create(ctx, runtime.Spec{Name: cname, Image: c.tpl.Image, Env: c.tpl.Env(creds), Cmd: cmdOf(c.tpl, creds)}); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rt.Remove(context.Background(), cname) }()
			if err := rt.Start(ctx, cname); err != nil {
				t.Fatal(err)
			}
			ready := func() bool { _, err := rt.Exec(ctx, cname, c.tpl.ReadyCmd(creds)); return err == nil }
			for i, ok := 0, 0; ok < 2; i++ { // two passes a few seconds apart: init scripts restart some servers once
				if i > 90 {
					t.Fatal("never became ready")
				}
				time.Sleep(2 * time.Second)
				if ready() {
					ok++
				} else {
					ok = 0
				}
			}
			run := func(cmd []string) string {
				out, err := rt.ExecEnv(ctx, cname, cmd, c.env)
				if err != nil {
					t.Fatalf("%v: %v: %s", cmd[0], err, out)
				}
				return strings.TrimSpace(out)
			}
			run(c.seed)
			if got := lastLine(run(c.count)); got != c.want {
				t.Fatalf("seeded count %q, want %s", got, c.want)
			}
			eng, _ := engineFor(name)
			if err := eng.check(creds); err != nil {
				t.Fatal(err)
			}
			data, err := eng.dump(ctx, rt, cname, creds)
			if err != nil || len(data) == 0 {
				t.Fatalf("dump: %d bytes, %v", len(data), err)
			}
			run(c.wipe)
			// A wiped SQL table makes the count query fail; that counts as "gone".
			if out, err := rt.ExecEnv(ctx, cname, c.count, c.env); err == nil && lastLine(out) == c.want {
				t.Fatalf("wipe did not remove the data (%q)", out)
			}
			if err := eng.restore(ctx, rt, cname, creds, data); err != nil {
				t.Fatalf("restore: %v", err)
			}
			for i := 0; i < 30 && !ready(); i++ { // redis restarts during restore
				time.Sleep(time.Second)
			}
			if got := lastLine(run(c.count)); got != c.want {
				t.Fatalf("after restore %q, want %s", got, c.want)
			}
		})
	}
}

func cmdOf(t services.Template, c map[string]string) []string {
	if t.Cmd == nil {
		return nil
	}
	return t.Cmd(c)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
