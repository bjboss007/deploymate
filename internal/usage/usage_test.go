package usage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	// stable order for comparison
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestParsers(t *testing.T) {
	if got := keys(ParsePostgres("172.25.0.13/32\n172.25.0.8\n\n::1/128\n")); !reflect.DeepEqual(got, []string{"172.25.0.13", "172.25.0.8"}) {
		t.Errorf("postgres = %v", got)
	}
	redis := "id=5 addr=172.25.0.6:52344 laddr=172.25.0.4:6379 fd=8 name= age=10\nid=6 addr=127.0.0.1:41000 fd=9\n"
	if got := keys(ParseRedis(redis)); !reflect.DeepEqual(got, []string{"127.0.0.1", "172.25.0.6"}) {
		t.Errorf("redis = %v", got)
	}
	if got := keys(ParseMySQL("172.25.0.20:51234\nlocalhost\nevent_scheduler\n")); !reflect.DeepEqual(got, []string{"172.25.0.20"}) {
		t.Errorf("mysql = %v", got)
	}
}

type fakeExec struct {
	out string
	err error
	cmd []string
	env []string
}

func (f *fakeExec) Exec(_ context.Context, _ string, cmd []string) (string, error) {
	f.cmd = cmd
	return f.out, f.err
}
func (f *fakeExec) ExecEnv(_ context.Context, _ string, cmd, env []string) (string, error) {
	f.cmd, f.env = cmd, env
	return f.out, f.err
}

func TestClients(t *testing.T) {
	f := &fakeExec{out: "172.25.0.13/32\n"}
	ips, ok := Clients(context.Background(), f, "postgres", "dm-svc-pg", map[string]string{"user": "dm"})
	if !ok || !ips["172.25.0.13"] || f.cmd[0] != "psql" || f.cmd[2] != "dm" {
		t.Errorf("postgres: %v %v %v", ips, ok, f.cmd)
	}
	f = &fakeExec{out: "id=1 addr=10.1.1.1:5000\n"}
	if ips, ok = Clients(context.Background(), f, "redis", "r", nil); !ok || !ips["10.1.1.1"] {
		t.Errorf("redis: %v %v", ips, ok)
	}
	f = &fakeExec{out: "10.2.2.2:1\n"}
	if ips, ok = Clients(context.Background(), f, "mysql", "m", map[string]string{"root_password": "pw"}); !ok || !ips["10.2.2.2"] || !strings.Contains(strings.Join(f.env, ","), "MYSQL_PWD=pw") {
		t.Errorf("mysql: %v %v env=%v", ips, ok, f.env)
	}
	// A failing probe means "unknown", never "nobody".
	f = &fakeExec{err: errors.New("exec failed")}
	if ips, ok = Clients(context.Background(), f, "redis", "r", nil); ok || ips != nil {
		t.Errorf("failed probe = %v, %v; want unknown", ips, ok)
	}
	if _, ok = Clients(context.Background(), &fakeExec{}, "mongo", "x", nil); ok {
		t.Error("unprobed types are unknown")
	}
}
