package backup

import (
	"context"
	"strings"
	"testing"
)

func newRT() *fakeRT { return &fakeRT{running: true, files: map[string][]byte{}} }

func scripted(rt *fakeRT, dump string) {
	rt.execFn = func(c execCall) (string, error) {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if len(c.cmd) > 0 && c.cmd[0] == "rm" {
			delete(rt.files, c.cmd[len(c.cmd)-1])
		} else {
			rt.files[containerDump] = []byte(dump)
		}
		return "", nil
	}
}

func TestMySQLDumpUsesRootThroughTheEnvironment(t *testing.T) {
	rt := newRT()
	scripted(rt, "-- sql")
	data, err := mysqlEngine{}.dump(context.Background(), rt, "c", map[string]string{"root_password": "r00t", "db": "app"})
	if err != nil || string(data) != "-- sql" {
		t.Fatalf("%q %v", data, err)
	}
	c := rt.execCalls()[0]
	cmd := strings.Join(c.cmd, " ")
	if !strings.HasPrefix(cmd, "mysqldump -uroot --single-transaction --routines --triggers --events") || !strings.HasSuffix(cmd, " app") {
		t.Fatalf("command: %s", cmd)
	}
	if strings.Contains(cmd, "r00t") || len(c.env) != 1 || c.env[0] != "MYSQL_PWD=r00t" {
		t.Fatalf("the password belongs in the environment, not argv: %v %v", c.cmd, c.env)
	}
}

func TestMySQLRestoreDropsCreatesThenReplays(t *testing.T) {
	rt := newRT()
	rt.execFn = func(execCall) (string, error) { return "", nil }
	if err := (mysqlEngine{}).restore(context.Background(), rt, "c", map[string]string{"root_password": "r", "db": "app"}, []byte("sql")); err != nil {
		t.Fatal(err)
	}
	calls := rt.execCalls()
	if len(calls) != 3 || !strings.Contains(calls[0].cmd[3], "DROP DATABASE IF EXISTS `app`; CREATE DATABASE `app`") ||
		!strings.Contains(calls[1].cmd[2], "< "+containerRestore) {
		t.Fatalf("steps: %v", calls)
	}
}

func TestMySQLRejectsABadDatabaseName(t *testing.T) {
	if (mysqlEngine{}).check(map[string]string{"root_password": "r", "db": "app`; DROP"}) == nil {
		t.Fatal("identifiers go into SQL: they must be validated")
	}
}

func TestRedisDumpAuthenticatesThroughTheEnvironment(t *testing.T) {
	rt := newRT()
	scripted(rt, "REDIS0011")
	if _, err := (redisEngine{}).dump(context.Background(), rt, "c", map[string]string{"password": "pw"}); err != nil {
		t.Fatal(err)
	}
	c := rt.execCalls()[0]
	if strings.Join(c.cmd, " ") != "redis-cli --rdb "+containerDump || len(c.env) != 1 || c.env[0] != "REDISCLI_AUTH=pw" {
		t.Fatalf("%v %v", c.cmd, c.env)
	}
	rt = newRT()
	scripted(rt, "x")
	_, _ = (redisEngine{}).dump(context.Background(), rt, "c", map[string]string{})
	if len(rt.execCalls()[0].env) != 0 {
		t.Fatal("a password-less Redis needs no auth variable")
	}
}

type stopStartRT struct {
	*fakeRT
	order []string
	fail  string
}

func (r *stopStartRT) Stop(_ context.Context, _ string, _ int) error {
	r.order = append(r.order, "stop")
	return nil
}
func (r *stopStartRT) Start(context.Context, string) error {
	r.order = append(r.order, "start")
	return nil
}

func TestRedisRestoreStopsThenWritesThenStarts(t *testing.T) {
	rt := &stopStartRT{fakeRT: newRT()}
	if err := (redisEngine{}).restore(context.Background(), rt, "c", nil, []byte("rdb")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(rt.order, ",") != "stop,start" || string(rt.files[redisData]) != "rdb" {
		t.Fatalf("order %v, file %q", rt.order, rt.files[redisData])
	}
}

func TestRedisRestoreStartsAgainWhenTheCopyFails(t *testing.T) {
	rt := &stopStartRT{fakeRT: newRT()}
	rt.writeErr = context.DeadlineExceeded
	if err := (redisEngine{}).restore(context.Background(), rt, "c", nil, []byte("rdb")); err == nil {
		t.Fatal("expected an error")
	}
	if strings.Join(rt.order, ",") != "stop,start" {
		t.Fatalf("the cache must not stay down: %v", rt.order)
	}
}

func TestMongoDumpAndRestoreCommands(t *testing.T) {
	creds := map[string]string{"user": "dm", "password": "p@ss/w", "db": "app"}
	if err := (mongoEngine{}).check(creds); err != nil {
		t.Fatal(err)
	}
	rt := newRT()
	scripted(rt, "archive")
	if _, err := (mongoEngine{}).dump(context.Background(), rt, "c", creds); err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(rt.execCalls()[0].cmd, " ")
	if !strings.Contains(cmd, "mongodump --uri=mongodb://dm:p%40ss%2Fw@127.0.0.1:27017/?authSource=admin --db=app --archive="+containerDump) {
		t.Fatalf("dump: %s", cmd)
	}
	rt = newRT()
	rt.execFn = func(execCall) (string, error) { return "", nil }
	if err := (mongoEngine{}).restore(context.Background(), rt, "c", creds, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rt.execCalls()[0].cmd, " "); !strings.Contains(got, "--drop --nsInclude=app.*") {
		t.Fatalf("restore: %s", got)
	}
}

func TestEveryProvisionableDatabaseHasAnEngine(t *testing.T) {
	for _, typ := range []string{"postgres", "mysql", "redis", "mongodb"} {
		if _, ok := engineFor(typ); !ok {
			t.Errorf("no backup engine for %s", typ)
		}
	}
	if _, ok := engineFor("nginx"); ok {
		t.Error("nginx has no data to back up")
	}
}
