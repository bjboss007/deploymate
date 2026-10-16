package services

import (
	"strings"
	"testing"
)

func TestMongoDBTemplate(t *testing.T) {
	tpl, ok := ForType("mongodb")
	if !ok || tpl.Image != "mongo:7.0" || tpl.URLEnv != "MONGODB_URL" {
		t.Fatalf("%+v %v", tpl, ok)
	}
	c := map[string]string{"user": "dm", "password": "p/w", "db": "app"}
	if got := tpl.ConnURL(c, "dm-mongo"); got != "mongodb://dm:p%2Fw@dm-mongo:27017/app?authSource=admin" {
		t.Errorf("url: %s", got)
	}
	comp := tpl.Components(c, "dm-mongo")
	if comp["MONGODB_HOST"] != "dm-mongo" || comp["MONGODB_PORT"] != "27017" || comp["MONGODB_USER"] != "dm" || comp["MONGODB_PASSWORD"] != "p/w" || comp["MONGODB_NAME"] != "app" {
		t.Errorf("components: %v", comp)
	}
	// The password is a separate argument to sh, never part of the script text.
	ready := tpl.ReadyCmd(c)
	if strings.Contains(ready[2], "p/w") || ready[len(ready)-1] != "p/w" {
		t.Errorf("ready command: %v", ready)
	}
	if env := strings.Join(tpl.Env(c), " "); !strings.Contains(env, "MONGO_INITDB_ROOT_PASSWORD=p/w") {
		t.Errorf("env: %s", env)
	}
}
