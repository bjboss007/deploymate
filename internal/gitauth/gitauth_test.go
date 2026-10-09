package gitauth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/githubapp"
	"github.com/habibmuhammad/deploymate/internal/store"
)

type env struct {
	r      *Resolver
	st     *store.Store
	key    [32]byte
	calls  *atomic.Int32
	status *atomic.Int32 // what the token endpoint answers (0 = 201)
}

func newEnv(t *testing.T, connect bool) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var key [32]byte
	rand.Read(key[:])
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))

	calls, status := &atomic.Int32{}, &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := githubapp.VerifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &k.PublicKey, time.Now()); err != nil {
			w.WriteHeader(401)
			return
		}
		calls.Add(1)
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
			w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"token":"ghs_abc","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
	}))
	t.Cleanup(srv.Close)

	if connect {
		pe, _ := crypto.Encrypt(key, pemKey)
		we, _ := crypto.Encrypt(key, "wh")
		if err := st.SaveGitHubApp(store.GitHubApp{AppID: 7, Slug: "x", Name: "x", HTMLURL: "https://github.com/apps/x", PEMEnc: pe, WebhookSecretEnc: we}); err != nil {
			t.Fatal(err)
		}
	}
	return &env{r: New(st, key, srv.URL), st: st, key: key, calls: calls, status: status}
}

func TestDeployKeySourcesKeepUsingTheirKey(t *testing.T) {
	e := newEnv(t, false)
	enc, _ := crypto.Encrypt(e.key, "PRIVATE-KEY-PEM")
	a, err := e.r.For(context.Background(), store.GitSource{CloneMethod: "deploy_key", PrivateKeyEnc: enc})
	if err != nil || a.KeyPEM != "PRIVATE-KEY-PEM" || a.Token != "" {
		t.Fatalf("auth = %+v, %v", a, err)
	}
}

func TestGitHubAppSourcesGetACachedInstallationToken(t *testing.T) {
	e := newEnv(t, true)
	gs := store.GitSource{CloneMethod: store.CloneGitHubApp, InstallationID: 9}
	for i := 0; i < 3; i++ {
		a, err := e.r.For(context.Background(), gs)
		if err != nil || a.Token != "ghs_abc" || a.KeyPEM != "" {
			t.Fatalf("auth = %+v, %v", a, err)
		}
	}
	if e.calls.Load() != 1 {
		t.Errorf("GitHub was asked for a token %d times, want 1 (cached)", e.calls.Load())
	}
	// A different installation is its own token.
	if _, err := e.r.For(context.Background(), store.GitSource{CloneMethod: store.CloneGitHubApp, InstallationID: 10}); err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", e.calls.Load())
	}
}

func TestAForgottenTokenIsFetchedAgain(t *testing.T) {
	e := newEnv(t, true)
	gs := store.GitSource{CloneMethod: store.CloneGitHubApp, InstallationID: 9}
	if _, err := e.r.For(context.Background(), gs); err != nil {
		t.Fatal(err)
	}
	e.r.Tokens.Forget(9)
	if _, err := e.r.For(context.Background(), gs); err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 2 {
		t.Errorf("a forgotten token must be fetched again (calls=%d)", e.calls.Load())
	}
}

func TestMissingOrUninstalledAppGivesPlainErrors(t *testing.T) {
	disconnected := newEnv(t, false)
	if _, err := disconnected.r.For(context.Background(), store.GitSource{CloneMethod: store.CloneGitHubApp, InstallationID: 9}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("not connected: %v", err)
	}
	e := newEnv(t, true)
	e.status.Store(404) // installation gone
	_, err := e.r.For(context.Background(), store.GitSource{CloneMethod: store.CloneGitHubApp, InstallationID: 9})
	if err == nil || !strings.Contains(err.Error(), "uninstalled") {
		t.Errorf("uninstalled: %v", err)
	}
	e.status.Store(500)
	_, err = e.r.For(context.Background(), store.GitSource{CloneMethod: store.CloneGitHubApp, InstallationID: 11})
	if err == nil || strings.Contains(err.Error(), "uninstalled") {
		t.Errorf("a GitHub outage must not be reported as an uninstall: %v", err)
	}
}
