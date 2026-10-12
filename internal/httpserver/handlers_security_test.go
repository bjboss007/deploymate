package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestSecurityAPIReportsRedisAndExposedContainers(t *testing.T) {
	e := newPrebuiltEnv(t)
	fake := &fakeRuntime{info: runtime.Info{Running: true, State: "running",
		PublicBindings: []string{"0.0.0.0:8080 (8080/tcp)"}, Privileged: false, NoNewPrivileges: false}}
	e.s.rt = fake
	e.s.SetSecurityContext("127.0.0.1:8080", false)
	e.st.CreateService(store.Service{ProjectID: e.app.ProjectID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: "dev", Port: 6379})
	tok := e.createToken(t, "claude", "read", "90")

	code, body := e.api(t, http.MethodGet, "/api/v1/security", tok)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	m := decode(t, body)
	if m["status"] != "critical" {
		t.Errorf("a published container port is critical, got %v", m["status"])
	}
	ids := map[string]string{}
	for _, c := range m["checks"].([]any) {
		cm := c.(map[string]any)
		ids[cm["id"].(string)] = cm["level"].(string)
	}
	if ids["containers-public"] != "critical" {
		t.Errorf("containers-public: %v", ids)
	}
	if ids["redis"] != "warning" {
		t.Errorf("a Redis without a password must be flagged: %v", ids)
	}
	if ids["dashboard"] != "ok" {
		t.Errorf("a loopback dashboard is fine: %v", ids)
	}
	if code, _ := e.api(t, http.MethodGet, "/api/v1/security", ""); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
}

func TestSecurityPageRendersTheChecks(t *testing.T) {
	e := newWebhookEnv(t)
	e.s.rt = &fakeRuntime{inspectErr: runtime.ErrContainerNotFound}
	e.s.SetSecurityContext("0.0.0.0:8080", true)
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	req := httptest.NewRequest(http.MethodGet, "/security", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"Security", "Dashboard address", "listens on every network interface", "DEPLOYMATE_ADDR=127.0.0.1:8080", "Tunnel mode"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Without a session the page is not served.
	rec2 := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/security", nil))
	if rec2.Code == http.StatusOK {
		t.Error("the security page must need a session")
	}
}

// ---- securing an existing Redis ------------------------------------------------

func TestAddingAPasswordToAnExistingRedisRestartsItAndRedeploysItsApps(t *testing.T) {
	e := newWebhookEnv(t)
	rt := &fakeRuntime{}
	e.s.rt = rt
	e.s.prov = services.NewProvisioner(e.st, rt, e.s.encKey, "test-net")
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})

	svc, _ := e.st.CreateService(store.Service{ProjectID: proj.ID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: store.EnvProduction, Port: 6379})
	empty, _ := crypto.Encrypt(e.s.encKey, "")
	e.st.SetServiceCredentials(svc.ID, map[string]string{"password": empty})

	mkApp := func(slug, env string) store.App {
		a, err := e.st.CreateApp(store.App{ProjectID: proj.ID, Name: slug, Slug: slug, Port: 8080, Status: "running", Environment: env})
		if err != nil {
			t.Fatal(err)
		}
		d, _ := e.st.CreateDeployment(store.Deployment{AppID: a.ID, Kind: "deploy", Status: "running", ImageTag: "img:" + slug})
		e.st.SetAppCurrentDeployment(a.ID, d.ID)
		a, _ = e.st.GetAppByID(a.ID)
		return a
	}
	uses := mkApp("uses", store.EnvProduction)
	otherEnv := mkApp("staging-app", store.EnvStaging)

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/services/cache/secure", strings.NewReader("csrf_token=csrf"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	// The page offers it while there is no password.
	getPage := func() string {
		req := httptest.NewRequest(http.MethodGet, "/services/cache", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if !strings.Contains(getPage(), "This Redis has no password") {
		t.Fatal("the service page should offer to add a password")
	}

	rec := post()
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "Redeploying+1") {
		t.Fatalf("secure: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	enc, _ := e.st.GetServiceCredentials(svc.ID)
	pw := e.s.decryptCreds(enc)["password"]
	if len(pw) < 32 {
		t.Fatalf("a password was not stored: %q", pw)
	}
	if len(rt.lastSpec.Cmd) != 3 || rt.lastSpec.Cmd[2] != pw {
		t.Errorf("Redis was not recreated with the new password: %v", rt.lastSpec.Cmd)
	}
	if ds, _ := e.st.ListDeployments(uses.ID, 5); len(ds) != 2 || ds[0].Kind != "redeploy" || ds[0].Status != "queued" {
		t.Errorf("the app that uses Redis must be redeployed: %+v", ds)
	}
	if ds, _ := e.st.ListDeployments(otherEnv.ID, 5); len(ds) != 1 {
		t.Errorf("an app in another environment does not receive this service: %+v", ds)
	}
	if strings.Contains(getPage(), "This Redis has no password") {
		t.Error("the offer must disappear once there is a password")
	}
	// The app now receives the password as separate variables too.
	appEnv := strings.Join(e.s.AppEnv(uses), "\n")
	for _, want := range []string{"REDIS_HOST=dm-svc-cache", "REDIS_PORT=6379", "REDIS_PASSWORD=" + pw, "REDIS_URL=redis://:" + pw + "@dm-svc-cache:6379/0"} {
		if !strings.Contains(appEnv, want) {
			t.Errorf("app environment lacks %q", want)
		}
	}
	// A second press changes nothing.
	again := post()
	if !strings.Contains(again.Header().Get("Location"), "already+has+a+password") {
		t.Errorf("second press: %s", again.Header().Get("Location"))
	}
	enc2, _ := e.st.GetServiceCredentials(svc.ID)
	if e.s.decryptCreds(enc2)["password"] != pw {
		t.Error("the password was changed by a second press")
	}
}

func TestRemovingTheRedisPasswordIsTheWayBack(t *testing.T) {
	e := newWebhookEnv(t)
	rt := &fakeRuntime{}
	e.s.rt = rt
	e.s.prov = services.NewProvisioner(e.st, rt, e.s.encKey, "test-net")
	owner, _ := e.st.CreateUser(store.User{Email: "o@test.dev", PasswordHash: "x", Role: "owner"})
	e.st.CreateSession(store.Session{UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	proj, _ := e.st.CreateProject(store.Project{UserID: owner.ID, Name: "P", Slug: "p"})
	svc, _ := e.st.CreateService(store.Service{ProjectID: proj.ID, Type: "redis", Name: "cache", Slug: "cache", Image: "redis:7", Status: "running", Environment: store.EnvProduction, Port: 6379})
	pwEnc, _ := crypto.Encrypt(e.s.encKey, "had-a-password")
	e.st.SetServiceCredentials(svc.ID, map[string]string{"password": pwEnc})
	a, _ := e.st.CreateApp(store.App{ProjectID: proj.ID, Name: "uses", Slug: "uses", Port: 8080, Status: "running", Environment: store.EnvProduction})
	d, _ := e.st.CreateDeployment(store.Deployment{AppID: a.ID, Kind: "deploy", Status: "running", ImageTag: "img:uses"})
	e.st.SetAppCurrentDeployment(a.ID, d.ID)

	post := func(path string) string {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("csrf_token=csrf"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}
	page := func() string {
		req := httptest.NewRequest(http.MethodGet, "/services/cache", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if !strings.Contains(page(), "Remove password") {
		t.Fatal("a protected Redis should offer to remove its password")
	}
	if loc := post("/services/cache/unsecure"); !strings.Contains(loc, "removed") || !strings.Contains(loc, "Redeploying+1") {
		t.Fatalf("remove: %s", loc)
	}
	enc, _ := e.st.GetServiceCredentials(svc.ID)
	if e.s.decryptCreds(enc)["password"] != "" {
		t.Error("the password is still stored")
	}
	if len(rt.lastSpec.Cmd) != 0 {
		t.Errorf("Redis was not recreated without --requirepass: %v", rt.lastSpec.Cmd)
	}
	if ds, _ := e.st.ListDeployments(a.ID, 5); len(ds) != 2 || ds[0].Kind != "redeploy" {
		t.Errorf("the app must be redeployed to drop the old URL: %+v", ds)
	}
	env := strings.Join(e.s.AppEnv(a), "\n")
	if strings.Contains(env, "REDIS_PASSWORD") || !strings.Contains(env, "REDIS_URL=redis://dm-svc-cache:6379/0") {
		t.Errorf("after removing the password the app environment is: %s", env)
	}
	if !strings.Contains(page(), "This Redis has no password") || strings.Contains(page(), "Remove password") {
		t.Error("the page should be back to offering Add a password")
	}
	if loc := post("/services/cache/unsecure"); !strings.Contains(loc, "has+no+password") {
		t.Errorf("a second removal: %s", loc)
	}
	// And the whole circle: add again.
	if loc := post("/services/cache/secure"); !strings.Contains(loc, "requires+a+password") {
		t.Errorf("add again: %s", loc)
	}
}
