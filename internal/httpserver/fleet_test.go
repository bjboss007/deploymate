package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

func TestAttentionFor(t *testing.T) {
	failed := &store.Deployment{Status: "failed", Error: "swap: staged container failed readiness probe: EOF"}
	ok := &store.Deployment{Status: "running"}
	for _, tc := range []struct {
		name      string
		app       store.App
		last      *store.Deployment
		level     string
		reasonHas string
	}{
		{"healthy", store.App{Status: "running", Health: "healthy"}, ok, "", ""},
		{"stopped is not an alarm", store.App{Status: "stopped"}, nil, "", ""},
		{"failed with a failed deploy", store.App{Status: "failed"}, failed, "bad", "readiness probe"},
		{"failed, container gone", store.App{Status: "failed"}, ok, "bad", "Not running"},
		{"unhealthy", store.App{Status: "running", Health: "unhealthy"}, ok, "bad", "health check"},
		{"deploy failed, old version serving", store.App{Status: "running", Health: "healthy"}, failed, "warn", "previous version is still serving"},
	} {
		level, reason := attentionFor(tc.app, tc.last)
		if level != tc.level || !strings.Contains(reason, tc.reasonHas) {
			t.Errorf("%s: got (%q, %q), want level %q containing %q", tc.name, level, reason, tc.level, tc.reasonHas)
		}
	}
}

// TestFleetBoard: the projects page leads with what needs attention (with
// the real failure reason), summarises the fleet in one line, and draws a
// 24-cell heartbeat per app; the project page keeps create forms out of the
// way behind disclosure buttons.
func TestFleetBoard(t *testing.T) {
	st, good := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Health: "healthy", Port: 8080})
	bad, err := st.CreateApp(store.App{ProjectID: good.ProjectID, Name: "Erp", Slug: "erp", Status: "failed", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	idle, err := st.CreateApp(store.App{ProjectID: good.ProjectID, Name: "Idle", Slug: "idle", Status: "stopped", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	_ = idle
	if _, err := st.CreateDeployment(store.Deployment{AppID: bad.ID, Kind: "deploy", Status: "failed", Error: "readiness probe EOF: the app exited"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateDeployment(store.Deployment{AppID: good.ID, Kind: "manual", Status: "running", ImageTag: "nginx:1.27"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}

	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	board := get("/projects")
	for _, want := range []string{
		"Needs attention", "readiness probe EOF: the app exited", // the real reason, up front
		"1 app healthy", "1 need attention", "1 stopped", // the one-line summary
		"nginx:1.27", "Never deployed",
	} {
		if !strings.Contains(board, want) {
			t.Errorf("fleet board lacks %q", want)
		}
	}
	if n := strings.Count(board, `class="strip"`); n != 3 { // each app once: the failing one only under "Needs attention"
		t.Errorf("strips rendered = %d, want 3", n)
	}
	if !strings.Contains(board, "listed under Needs attention") || !strings.Contains(board, "in Test") {
		t.Error("the project list should point at the attention list, and attention rows should name their project")
	}
	if n := strings.Count(board, `class="hb `); n < 24*3 {
		t.Errorf("heartbeat cells = %d, want 24 per strip", n)
	}

	page := get("/projects/test")
	for _, want := range []string{"New app", "New database or cache", "Delete project", "readiness probe EOF"} {
		if !strings.Contains(page, want) {
			t.Errorf("project page lacks %q", want)
		}
	}
	// Create forms live inside <details> — not the first thing on the page.
	if strings.Index(page, "<details") < 0 || strings.Index(page, `name="name"`) < strings.Index(page, "<details") {
		t.Error("create forms must sit behind a disclosure button")
	}
}

// TestDeploymentPageExplainsFailure: a failed deployment shows the raw error,
// what it usually means, links to the right app tabs, and what is serving.
func TestDeploymentPageExplainsFailure(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Erp", Slug: "erp", Status: "running", Port: 8080})
	d, err := st.CreateDeployment(store.Deployment{
		AppID: app.ID, Kind: "deploy", Status: "failed", Trigger: "dashboard", CommitSHA: "17177b387e9d", CommitMessage: "Add deploymate.yml",
		Error: `swap: staged container failed readiness probe: Get "http://127.0.0.1:1/": EOF — the container exited with code 1`,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	req := httptest.NewRequest(http.MethodGet, "/deployments/"+d.ID, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"What went wrong", "the container exited with code 1", // the raw error
		"The app crashed while starting",                 // what it usually means
		`href="/apps/erp#variables"`,                     // a link to the fix
		"Failed — the previous version is still serving", // what is serving
		"Add deploymate.yml", "Jump to first error", "Copy log",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("deployment page lacks %q", want)
		}
	}
}

// Unknown addresses (and apps that no longer exist) get the designed
// not-found screen with a way back, not the bare "404 page not found" text.
func TestNotFoundPage(t *testing.T) {
	st, _ := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Port: 8080})
	s := &Server{store: st}
	for _, path := range []string{"/definitely-not-a-page", "/apps/ghost"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Page not found") || !strings.Contains(rec.Body.String(), `href="/projects"`) {
			t.Errorf("%s: status %d, body lacks the not-found screen", path, rec.Code)
		}
	}
}

// TestRedeployQueuesCurrentImage: "Redeploy" re-runs the version the app is
// serving (so new settings apply without a build or a new CI run) and refuses
// politely when nothing is deployed.
func TestRedeployQueuesCurrentImage(t *testing.T) {
	e := newPrebuiltEnv(t)
	post := func() (int, string) { return e.post(t, "/apps/api/redeploy", url.Values{}) }

	if _, loc := post(); !strings.Contains(flashOf(t, loc), "Nothing is deployed yet") {
		t.Errorf("no current deployment: flash = %q", flashOf(t, loc))
	}

	cur, err := e.st.CreateDeployment(store.Deployment{AppID: e.app.ID, Kind: "deploy", Status: "running", ImageTag: "deploymate/apps/api:abc", CommitSHA: "17177b38", CommitMessage: "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAppCurrentDeployment(e.app.ID, cur.ID); err != nil {
		t.Fatal(err)
	}
	code, loc := post()
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/deployments/") {
		t.Fatalf("redeploy: %d %q", code, loc)
	}
	ds, _ := e.st.ListDeployments(e.app.ID, 5)
	if len(ds) != 2 {
		t.Fatalf("deployments = %d, want 2", len(ds))
	}
	d := ds[0]
	if d.Kind != "redeploy" || d.Status != "queued" || d.ImageTag != "deploymate/apps/api:abc" || d.CommitSHA != "17177b38" || d.CommitMessage != "ship it" {
		t.Errorf("redeploy row = %+v", d)
	}
}

// Variables saved after the running deployment was created only apply to a
// new container, so the app page says so and offers Redeploy.
func TestPendingEnvBanner(t *testing.T) {
	e := newPrebuiltEnv(t)
	cur, err := e.st.CreateDeployment(store.Deployment{AppID: e.app.ID, Kind: "deploy", Status: "running", ImageTag: "deploymate/apps/api:abc"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAppCurrentDeployment(e.app.ID, cur.ID); err != nil {
		t.Fatal(err)
	}
	page := func() string {
		req := httptest.NewRequest(http.MethodGet, "/apps/api", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /apps/api: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if strings.Contains(page(), "Variables changed since the last deploy") {
		t.Fatal("banner shown with no variable changes")
	}
	time.Sleep(5 * time.Millisecond) // the event must be strictly newer than the deployment
	e.post(t, "/apps/api/env/bulk", url.Values{"key_0": {"A"}, "value_0": {"1"}})
	body := page()
	if !strings.Contains(body, "Variables changed since the last deploy") || !strings.Contains(body, `action="/apps/api/redeploy"`) {
		t.Error("banner/Redeploy missing after a variable change")
	}
}

// TestProjectPageGroupsAppsWithTheirResources: each app is one card carrying
// its identity colour and logo, with the databases/caches of its environment
// inside it; a service that is down turns its apps red; services no app gets
// are listed apart.
func TestProjectPageGroupsAppsWithTheirResources(t *testing.T) {
	st, web := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Health: "healthy", Port: 8080, Environment: "dev", Runtime: "node:22"})
	erp, err := st.CreateApp(store.App{ProjectID: web.ProjectID, Name: "Erp", Slug: "erp", Status: "running", Health: "healthy", Port: 8080, Environment: "dev", DeployMode: store.DeployModeArtifact, Stack: "spring"})
	if err != nil {
		t.Fatal(err)
	}
	_ = erp
	mk := func(name, slug, typ, image, status, env string) {
		if _, err := st.CreateService(store.Service{ProjectID: web.ProjectID, Type: typ, Name: name, Slug: slug, Image: image, Status: status, Environment: env, Port: 5432}); err != nil {
			t.Fatal(err)
		}
	}
	mk("dev-postgres", "dev-postgres", "postgres", "postgres:16-alpine", "running", "dev")
	mk("dev-redis", "dev-redis", "redis", "redis:7-alpine", "stopped", "dev")          // down
	mk("prod-cache", "prod-cache", "redis", "redis:7-alpine", "running", "production") // no production app -> unused

	s := &Server{store: st}
	req := httptest.NewRequest(http.MethodGet, "/projects/test", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`class="acard`, "Available to this app", // the card
		"Spring on Java 21", "Node.js 22", // stack labels (detected framework / runtime)
		"PostgreSQL 16", "Redis 7", "DATABASE_URL", "REDIS_URL", // resource chips
		"Running, but its Redis is down.", // a down resource is surfaced on the app
		"Stopped · start it",
		"Not used by any app in this project", "prod-cache", // services nobody gets
		`class="tile id-`, // identity colour on the logo tile
		`<path d="M`,      // a real logo mark is drawn
	} {
		if !strings.Contains(body, want) {
			t.Errorf("project page lacks %q", want)
		}
	}
	if n := strings.Count(body, `class="acard`); n < 2 {
		t.Errorf("app cards = %d, want 2", n)
	}
	if !strings.Contains(body, ">shared<") {
		t.Error("services used by both apps should be marked shared")
	}
}

func TestAppearanceHandler(t *testing.T) {
	e := newPrebuiltEnv(t)
	_, loc := e.post(t, "/apps/api/appearance", url.Values{"logo": {"react"}, "accent": {"pink"}})
	if !strings.Contains(flashOf(t, loc), "Appearance saved") {
		t.Errorf("flash = %q", flashOf(t, loc))
	}
	app, _ := e.reload(t)
	if app.Logo != "react" || app.Accent != "pink" {
		t.Fatalf("saved = %q / %q", app.Logo, app.Accent)
	}
	// Invalid choices are refused and change nothing.
	for _, bad := range []url.Values{{"logo": {"not-a-logo"}, "accent": {"pink"}}, {"logo": {"react"}, "accent": {"red"}}} {
		_, loc := e.post(t, "/apps/api/appearance", bad)
		if f := flashOf(t, loc); !strings.Contains(f, "isn't available") {
			t.Errorf("flash = %q", f)
		}
	}
	if app, _ = e.reload(t); app.Logo != "react" || app.Accent != "pink" {
		t.Errorf("a refused save changed the app: %q / %q", app.Logo, app.Accent)
	}
	// Back to automatic.
	e.post(t, "/apps/api/appearance", url.Values{"logo": {""}, "accent": {""}})
	if app, _ = e.reload(t); app.Logo != "" || app.Accent != "" {
		t.Errorf("automatic not restored: %q / %q", app.Logo, app.Accent)
	}
}

// usageRuntime reports per-container IPs and a Redis/Postgres client list.
type usageRuntime struct {
	fakeRuntime
	ips     map[string][]string // container name -> addresses
	clients string              // output of the probe (any service type)
}

func (u *usageRuntime) Inspect(_ context.Context, name string) (runtime.Info, error) {
	return runtime.Info{Running: true, IPs: u.ips[name]}, nil
}
func (u *usageRuntime) Exec(context.Context, string, []string) (string, error) { return u.clients, nil }

// TestResourceCardsShowRealUsageAndHonourOptOut: a chip says whether the app
// really holds a connection ("Connected now" / "Not connected right now"),
// and "Stop sending" removes the service from the app's injected environment
// (and its card), flags the app for a redeploy, and can be undone.
func TestResourceCardsShowRealUsageAndHonourOptOut(t *testing.T) {
	st, web := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Health: "healthy", Port: 8080, Environment: "dev"})
	api, err := st.CreateApp(store.App{ProjectID: web.ProjectID, Name: "Api", Slug: "api", Status: "running", Health: "healthy", Port: 8080, Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	redis, err := st.CreateService(store.Service{ProjectID: web.ProjectID, Type: "redis", Name: "dev-redis", Slug: "dev-redis", Image: "redis:7-alpine", Status: "running", Environment: "dev", Port: 6379})
	if err != nil {
		t.Fatal(err)
	}
	// Only api (172.25.0.9) is connected to the redis; web (172.25.0.8) is not.
	rt := &usageRuntime{
		ips:     map[string][]string{"dm-web": {"172.25.0.8"}, "dm-api": {"172.25.0.9"}},
		clients: "id=1 addr=172.25.0.9:5000 fd=8\n",
	}
	s := &Server{store: st, rt: rt, encKey: [32]byte{3}}
	enc, err := crypto.Encrypt(s.encKey, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetServiceCredentials(redis.ID, map[string]string{"password": enc}); err != nil {
		t.Fatal(err)
	}
	if env := strings.Join(s.AppEnv(web), "\n"); !strings.Contains(env, "REDIS_URL") {
		t.Fatalf("before opting out, web should receive REDIS_URL: %s", env)
	}
	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	post := func(path string, f url.Values) string {
		f.Set("csrf_token", "csrf")
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}

	page := get("/projects/test")
	if !strings.Contains(page, "Connected now") || !strings.Contains(page, "Not connected right now") {
		t.Errorf("usage states missing: connected=%v idle=%v", strings.Contains(page, "Connected now"), strings.Contains(page, "Not connected right now"))
	}
	if !strings.Contains(page, "Stop sending") {
		t.Error("each resource chip needs a Stop sending action")
	}

	// Web doesn't use Redis: stop sending it.
	loc := post("/apps/web/services/"+redis.ID+"/exclusion", url.Values{"excluded": {"1"}})
	if !strings.Contains(flashOf(t, loc), "no longer be sent to Web") {
		t.Errorf("flash = %q", flashOf(t, loc))
	}
	// Its environment no longer carries REDIS_URL — api's still does.
	if env := strings.Join(s.AppEnv(web), "\n"); strings.Contains(env, "REDIS_URL") {
		t.Errorf("web still receives REDIS_URL: %s", env)
	}
	if env := strings.Join(s.AppEnv(api), "\n"); !strings.Contains(env, "REDIS_URL") {
		t.Errorf("api lost REDIS_URL: %s", env)
	}
	page = get("/projects/test")
	if !strings.Contains(page, "Not sent to this app") || !strings.Contains(page, "Send again") {
		t.Error("the opted-out service should be listed as not sent, with Send again")
	}
	// A service from another project can't be attached.
	if loc := post("/apps/web/services/does-not-exist/exclusion", url.Values{"excluded": {"1"}}); loc != "" {
		t.Errorf("unknown service accepted: %q", loc)
	}
	// And it can be undone.
	post("/apps/web/services/"+redis.ID+"/exclusion", url.Values{"excluded": {"0"}})
	if ex, _ := st.ListAppServiceExclusions(web.ID); len(ex) != 0 {
		t.Errorf("exclusion not removed: %v", ex)
	}
}

// The Appearance panel says what was detected, and offers a way back to it
// only when the owner's choice overrides it.
func TestAppearancePanelShowsDetectedLogo(t *testing.T) {
	e := newPrebuiltEnv(t)
	if err := e.st.UpdateAppStack(e.app.ID, "spring"); err != nil {
		t.Fatal(err)
	}
	page := func() string {
		req := httptest.NewRequest(http.MethodGet, "/apps/api", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	body := page()
	if !strings.Contains(body, "Detected: Spring") || strings.Contains(body, "Use detected") {
		t.Error("detected framework should show without an override button")
	}
	e.post(t, "/apps/api/appearance", url.Values{"logo": {"react"}, "accent": {""}})
	body = page()
	if !strings.Contains(body, "Detected: Spring") || !strings.Contains(body, "overrides it") || !strings.Contains(body, "Use detected") {
		t.Error("an override should be flagged with a Use detected button")
	}
	e.post(t, "/apps/api/appearance", url.Values{"logo": {""}, "accent": {""}})
	if app, _ := e.reload(t); app.Logo != "" {
		t.Errorf("Use detected left logo %q", app.Logo)
	}
}

// An app that builds Java on this server gets low-memory advice on its page;
// a prebuilt app (CI builds it) and a roomy Docker do not.
type memRuntime struct {
	runtime.Runtime
	total uint64
}

func (m memRuntime) TotalMemory(context.Context) (uint64, error) { return m.total, nil }

func TestJVMMemoryNoteOnAppPage(t *testing.T) {
	e := newPrebuiltEnv(t)
	if err := e.st.UpdateAppStack(e.app.ID, "spring"); err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.Encrypt(e.s.encKey, testPEM(t)) // the page needs a real deploy key
	if _, err := e.st.DB().Exec(`UPDATE git_sources SET private_key_enc = ? WHERE id = ?`, key, e.gs.ID); err != nil {
		t.Fatal(err)
	}
	page := func() string {
		req := httptest.NewRequest(http.MethodGet, "/apps/api", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	e.s.rt = memRuntime{total: 3 << 30}
	if !strings.Contains(page(), "Heads-up: Docker has 3.0 GiB") {
		t.Error("low memory should show the advice")
	}
	e.s.rt = memRuntime{total: 7 << 30}
	if strings.Contains(page(), "Heads-up: Docker") {
		t.Error("enough memory should show nothing")
	}
	e.s.rt = memRuntime{total: 3 << 30}
	e.enablePrebuilt(t)
	if strings.Contains(page(), "Heads-up: Docker") {
		t.Error("a prebuilt app is not built here; no advice")
	}
}

// The service page lists the apps that receive it with their live connection
// state, shows an opted-out app as such, and Stop sending / Send again return
// to the service page.
func TestServicePageListsConsumersWithUsageAndOptOut(t *testing.T) {
	st, web := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Health: "healthy", Port: 8080, Environment: "dev"})
	if _, err := st.CreateApp(store.App{ProjectID: web.ProjectID, Name: "Api", Slug: "api", Status: "running", Health: "healthy", Port: 8080, Environment: "dev"}); err != nil {
		t.Fatal(err)
	}
	redis, err := st.CreateService(store.Service{ProjectID: web.ProjectID, Type: "redis", Name: "dev-redis", Slug: "dev-redis", Image: "redis:7-alpine", Status: "running", Environment: "dev", Port: 6379})
	if err != nil {
		t.Fatal(err)
	}
	rt := &usageRuntime{
		ips:     map[string][]string{"dm-web": {"172.25.0.8"}, "dm-api": {"172.25.0.9"}},
		clients: "id=1 addr=172.25.0.9:5000 fd=8\n",
	}
	s := &Server{store: st, rt: rt, encKey: [32]byte{3}}
	enc, _ := crypto.Encrypt(s.encKey, "")
	if err := st.SetServiceCredentials(redis.ID, map[string]string{"password": enc}); err != nil {
		t.Fatal(err)
	}
	get := func(path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	post := func(path string, f url.Values) string {
		f.Set("csrf_token", "csrf")
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}

	page := get("/services/dev-redis")
	for _, want := range []string{"Web", "Api", "Connected now", "Not connected right now", "Stop sending"} {
		if !strings.Contains(page, want) {
			t.Errorf("service page lacks %q", want)
		}
	}
	loc := post("/apps/web/services/"+redis.ID+"/exclusion", url.Values{"excluded": {"1"}, "back": {"/services/dev-redis"}})
	if !strings.HasPrefix(loc, "/services/dev-redis?flash=") {
		t.Errorf("redirect = %q, want back to the service page", loc)
	}
	page = get("/services/dev-redis")
	if !strings.Contains(page, "not sent to this app") || !strings.Contains(page, "Send again") {
		t.Error("an opted-out app should be shown as not sent, with Send again")
	}
	// An off-site or odd back target is ignored.
	for _, bad := range []string{"https://evil.test/", "//evil.test", "/services/x?y=1", "/apps/web"} {
		if loc := post("/apps/web/services/"+redis.ID+"/exclusion", url.Values{"excluded": {"0"}, "back": {bad}}); !strings.HasPrefix(loc, "/projects/") {
			t.Errorf("back=%q redirected to %q", bad, loc)
		}
	}
}

// Hand-created services pick their environment; an unspecified one stays
// production (the old behaviour) and an unknown one is refused.
func TestServiceCreateEnvironment(t *testing.T) {
	e := newPrebuiltEnv(t)
	for _, tc := range []struct{ name, env, want string }{
		{"Dev Cache", "dev", "dev"},
		{"Stage DB", "staging", "staging"},
		{"Old Client", "", "production"},
	} {
		form := url.Values{"name": {tc.name}, "type": {"redis"}}
		if tc.env != "" {
			form.Set("environment", tc.env)
		}
		_, loc := e.post(t, "/projects/test/services", form)
		if !strings.HasPrefix(loc, "/services/") {
			t.Fatalf("%s: redirect %q (%s)", tc.name, loc, flashOf(t, loc))
		}
		sv, err := e.st.GetServiceBySlug(strings.TrimPrefix(loc, "/services/"))
		if err != nil || sv.Environment != tc.want {
			t.Errorf("%s: environment = %q (%v), want %q", tc.name, sv.Environment, err, tc.want)
		}
	}
	_, loc := e.post(t, "/projects/test/services", url.Values{"name": {"Bad"}, "type": {"redis"}, "environment": {"qa"}})
	if !strings.Contains(flashOf(t, loc), "Unknown environment") {
		t.Errorf("unknown env flash = %q", flashOf(t, loc))
	}
}
