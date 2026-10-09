package gitpkg

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAuthEnvOnlyForHTTPSAndNeverInTheURL(t *testing.T) {
	a := Auth{Token: "ghs_secret"}
	env := strings.Join(a.env("https://github.com/acme/site.git"), "\n")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_secret"))
	for _, s := range []string{"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader", "GIT_CONFIG_VALUE_0=Authorization: " + want, "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(env, s) {
			t.Errorf("env missing %q:\n%s", s, env)
		}
	}
	for _, repo := range []string{"git@github.com:acme/site.git", "http://github.com/acme/site.git", "ssh://git@github.com/a/b.git", "not a url"} {
		if got := a.env(repo); got != nil {
			t.Errorf("a token must never be sent to %q (env %v)", repo, got)
		}
	}
	if (Auth{KeyPEM: "key"}).env("https://github.com/a/b.git") != nil {
		t.Error("a deploy key adds no HTTPS header")
	}
}

// The token is sent as a header on the very first request, and the clone leaves it
// nowhere on disk.
func TestCloneAuthSendsTheTokenAsAHeaderAndDoesNotStoreIt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("GIT_SSL_NO_VERIFY", "true")

	dest := filepath.Join(t.TempDir(), "checkout")
	err := CloneAuth(context.Background(), srv.URL+"/acme/site.git", "main", "", Auth{Token: "ghs_secret"}, dest)
	if err == nil {
		t.Fatal("the fake server has no repository, so the clone must fail")
	}
	mu.Lock()
	defer mu.Unlock()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:ghs_secret"))
	if len(seen) == 0 || seen[0] != want {
		t.Fatalf("first request's Authorization = %v, want %q", seen, want)
	}
	if strings.Contains(err.Error(), "ghs_secret") {
		t.Errorf("the error leaks the token: %v", err)
	}
	if b, rerr := os.ReadFile(filepath.Join(dest, ".git", "config")); rerr == nil && strings.Contains(string(b), "ghs_secret") {
		t.Error("the token was written to .git/config")
	}
}
