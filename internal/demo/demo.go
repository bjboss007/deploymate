// Package demo seeds a throwaway DeployMate with a believable fleet — fictional
// company, fictional apps, nothing from a real server — so the website's tour
// and any screenshot show the real dashboard on safe data. Everything is placed
// relative to "now" so the heartbeat strips look alive. Used by
// `deploymate seed-demo`; never run against a data dir you care about.
package demo

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/gitpkg"
	"github.com/habibmuhammad/deploymate/internal/store"
)

type seeder struct {
	st  *store.Store
	key [32]byte
	now time.Time
	err error
}

func (s *seeder) check(err error) {
	if s.err == nil && err != nil {
		_, file, line, _ := runtime.Caller(1)
		s.err = fmt.Errorf("demo seed (%s:%d): %w", filepath.Base(file), line, err)
	}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Seed fills an empty store. The owner is demo@deploymate.dev.
func Seed(st *store.Store, key [32]byte, now time.Time, passwordHash string) error {
	s := &seeder{st: st, key: key, now: now}
	owner, err := st.CreateUser(store.User{Email: "demo@deploymate.dev", PasswordHash: passwordHash, Role: "owner"})
	if err != nil {
		return err
	}

	// ---- Storefront: a typical web + API + worker with Postgres and Redis ----
	storefront := s.project(owner.ID, "Storefront")
	pg := s.service(storefront, "postgres", "Postgres", "postgres:16-alpine", store.EnvProduction, 5432)
	rd := s.service(storefront, "redis", "Redis", "redis:7-alpine", store.EnvProduction, 6379)
	s.service(storefront, "postgres", "Staging Postgres", "postgres:16-alpine", store.EnvStaging, 5432)
	_, _ = pg, rd

	web := s.app(storefront, appSpec{
		name: "Web", slug: "web", env: store.EnvProduction, runtime: "node:22", stack: "react", port: 3000,
		repo: "acme-demo/storefront-web", branch: "main", status: "running", health: "healthy",
		domains: []string{"shop.acme.example"},
	})
	api := s.app(storefront, appSpec{
		name: "API", slug: "api", env: store.EnvProduction, runtime: "java:21", stack: "spring", port: 8080,
		repo: "acme-demo/storefront-api", branch: "main", status: "running", health: "healthy",
		mode: store.DeployModeArtifact, domains: []string{"api.acme.example"}, replicas: 2,
	})
	worker := s.app(storefront, appSpec{
		name: "Worker", slug: "worker", env: store.EnvProduction, runtime: "python:3.13", stack: "", port: 8000,
		repo: "acme-demo/storefront-worker", branch: "main", status: "running", health: "healthy",
	})
	apiStaging := s.app(storefront, appSpec{
		name: "API (staging)", slug: "api-staging", env: store.EnvStaging, runtime: "java:21", stack: "spring", port: 8080,
		repo: "acme-demo/storefront-api", branch: "develop", status: "running", health: "healthy", mode: store.DeployModeArtifact,
	})

	// ---- Billing: one app has a failing deploy (the "needs attention" story) ----
	billing := s.project(owner.ID, "Billing")
	s.service(billing, "postgres", "Billing DB", "postgres:16-alpine", store.EnvProduction, 5432)
	ledger := s.app(billing, appSpec{
		name: "Ledger", slug: "ledger", env: store.EnvProduction, runtime: "java:21", stack: "spring", port: 8080,
		repo: "acme-demo/ledger", branch: "main", status: "running", health: "healthy", mode: store.DeployModeArtifact,
	})
	invoicer := s.app(billing, appSpec{
		name: "Invoicer", slug: "invoicer", env: store.EnvProduction, runtime: "node:22", stack: "next", port: 3000,
		repo: "acme-demo/invoicer", branch: "main", status: "running", health: "unhealthy",
	})

	// ---- Internal tools: an image app and a stopped one ----
	internal := s.project(owner.ID, "Internal tools")
	status := s.app(internal, appSpec{
		name: "Status page", slug: "status-page", env: store.EnvProduction, image: "nginx:1.27-alpine", port: 80,
		status: "running", health: "healthy", domains: []string{"status.acme.example"},
	})
	metrics := s.app(internal, appSpec{
		name: "Metrics", slug: "metrics", env: store.EnvProduction, image: "grafana/grafana:11.2.0", port: 3000,
		status: "stopped",
	})

	// ---- history ----
	h := func(d time.Duration) time.Time { return s.now.Add(-d) }

	// Web: steady, deployed a few times today, one deploy 2h ago is current.
	webCurrent := s.deploys(web, []dep{
		{h(23 * time.Hour), "Add order tracking page", "a1f9c3e", "webhook", ""},
		{h(15 * time.Hour), "Fix cart badge off-by-one", "b72d0e4", "webhook", ""},
		{h(9 * time.Hour), "Upgrade to Node 22", "c03aa18", "webhook", ""},
		{h(2 * time.Hour), "Lazy-load the product gallery", "d94be57", "webhook", ""},
	})
	s.logs(webCurrent, webDeployLog("acme-demo/storefront-web", "main", "d94be57", "Lazy-load the product gallery", "Node.js 22"))

	// API: prebuilt from CI; a brief unhealthy window 7h ago that recovered.
	s.deploys(api, []dep{
		{h(22 * time.Hour), "Bump spring-boot to 3.4.2", "e51c0aa", "ci", ""},
		{h(11 * time.Hour), "Add retry with backoff to payment webhook", "f8820bd", "ci", ""},
		{h(7*time.Hour + 30*time.Minute), "Cache the catalogue query", "0a7d2c1", "ci", ""},
	})
	s.event(api.id, store.EventHealthUnhealthy, "health probe failing: HTTP 503", h(7*time.Hour+20*time.Minute))
	s.event(api.id, store.EventHealthRecovered, "health probe OK again", h(7*time.Hour+12*time.Minute))
	s.event(api.id, store.EventEnvChanged, "env vars set: LOG_LEVEL, PAYMENTS_BASE_URL", h(19*time.Hour))

	s.deploys(apiStaging, []dep{
		{h(4 * time.Hour), "Try the new pricing rules", "7b9c3d6", "ci", ""},
		{h(70 * time.Minute), "Pricing rules: handle bundles", "8c0d2e7", "ci", ""},
	})
	s.deploys(worker, []dep{
		{h(20 * time.Hour), "Batch the nightly email digest", "1b3c9d0", "webhook", ""},
		{h(5 * time.Hour), "Skip empty digests", "2c4d8e1", "webhook", ""},
	})
	s.deploys(ledger, []dep{
		{h(21 * time.Hour), "Reconcile in a single transaction", "3d5e7f2", "ci", ""},
		{h(3 * time.Hour), "Round half-even in the tax calculation", "4e6f6a3", "ci", ""},
	})

	// Invoicer: the last deploy failed its readiness probe; the app went unhealthy.
	s.deploys(invoicer, []dep{
		{h(18 * time.Hour), "Render invoices as PDF server-side", "5f7a5b4", "webhook", ""},
	})
	failed := s.failedDeploy(invoicer, h(50*time.Minute), "Switch to the new tax service", "6a8b4c5",
		"readiness probe: Get \"http://172.18.0.9:3000/\": EOF")
	s.logs(failed, invoicerFailureLog())
	s.event(invoicer.id, store.EventHealthUnhealthy, "health probe failing: connection refused", h(46*time.Minute))

	s.deploys(status, []dep{{h(14 * time.Hour), "", "", "manual", ""}})
	s.deploys(metrics, []dep{{h(40 * time.Hour), "", "", "manual", ""}})
	s.event(metrics.id, store.EventAppStopped, "app stopped from the dashboard", h(9*time.Hour))

	// A few variables (names only matter; secrets are masked) and uptime history.
	s.envVars(api, map[string]string{"LOG_LEVEL": "info", "PAYMENTS_BASE_URL": "https://payments.example", "JWT_SECRET": "demo-not-a-real-secret"})
	s.envVars(web, map[string]string{"PUBLIC_API_URL": "https://api.acme.example"})
	s.uptime(web)
	s.uptime(api)
	s.uptime(status)

	// An agent's footprint: a read-only token and a deploy token with a short audit trail,
	// so the tokens page tells the "let an agent drive it" story.
	tokR, err := s.st.CreateAPIToken(store.APIToken{UserID: owner.ID, Name: "Claude (read-only)", TokenHash: "demo-hash-1", Prefix: "dm_Qk7Zp2", Scope: store.ScopeRead, ExpiresAt: ts(s.now.Add(80 * 24 * time.Hour))})
	s.check(err)
	tokD, err := s.st.CreateAPIToken(store.APIToken{UserID: owner.ID, Name: "Release agent", TokenHash: "demo-hash-2", Prefix: "dm_Ht4Wn9", Scope: store.ScopeDeploy, ExpiresAt: ts(s.now.Add(25 * 24 * time.Hour))})
	s.check(err)
	_, err = s.st.DB().Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ts(s.now.Add(-12*time.Minute)), tokR.ID)
	s.check(err)
	_, err = s.st.DB().Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, ts(s.now.Add(-41*time.Minute)), tokD.ID)
	s.check(err)
	for _, a := range []struct {
		at                          time.Duration
		action, target, detail, res string
	}{
		{41 * time.Minute, "redeploy", "api", "deployment 6c1f9e02", "ok"},
		{44 * time.Minute, "set_variables", "api", "LOG_LEVEL, PAYMENTS_BASE_URL", "ok"},
		{3 * time.Hour, "retry", "6c1f9e02", "Only the app's newest deployment can be retried.", "refused"},
		{5 * time.Hour, "deploy", "worker", "deployment 2c4d8e1a", "ok"},
	} {
		_, err := s.st.DB().Exec(`INSERT INTO audit_log (ts, token_id, token_name, action, target, detail, result) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ts(s.now.Add(-a.at)), tokD.ID, "Release agent", a.action, a.target, a.detail, a.res)
		s.check(err)
	}

	_ = metrics
	return s.err
}

// ---- builders ---------------------------------------------------------------

type appSpec struct {
	name, slug, env, runtime, stack, image, repo, branch, status, health, mode string
	port, replicas                                                             int
	domains                                                                    []string
}

type appRef struct {
	id   string
	slug string
}

func (s *seeder) project(userID, name string) store.Project {
	p, err := s.st.CreateProject(store.Project{UserID: userID, Name: name, Slug: strings.ToLower(strings.ReplaceAll(name, " ", "-"))})
	s.check(err)
	return p
}

func (s *seeder) service(p store.Project, typ, name, image, env string, port int) store.Service {
	slug := strings.ToLower(strings.ReplaceAll(name, " ", "-"))
	sv, err := s.st.CreateService(store.Service{
		ProjectID: p.ID, Type: typ, Name: name, Slug: p.Slug + "-" + slug, Image: image, Status: "running",
		VolumeName: "dm-svc-" + p.Slug + "-" + slug + "-data", Port: port, Environment: env, Origin: store.OriginManifest,
	})
	s.check(err)
	return sv
}

func (s *seeder) app(p store.Project, sp appSpec) appRef {
	if sp.replicas == 0 {
		sp.replicas = 1
	}
	if sp.mode == "" {
		sp.mode = store.DeployModeBuild
	}
	a, err := s.st.CreateApp(store.App{
		ProjectID: p.ID, Name: sp.name, Slug: sp.slug, Environment: sp.env, Runtime: sp.runtime, Image: sp.image,
		Port: sp.port, Status: sp.status, Health: sp.health, BuildType: "dockerfile", Replicas: sp.replicas,
		DeployMode: sp.mode, Stack: sp.stack, MemLimitMB: 512, CPULimit: 0.5,
	})
	s.check(err)
	if sp.mode == store.DeployModeArtifact {
		s.check(s.st.UpdateAppDeployMode(a.ID, sp.mode, "", ""))
	}
	if sp.repo != "" {
		dk, err := gitpkg.GenerateDeployKey()
		s.check(err)
		if dk != nil {
			priv, _ := crypto.Encrypt(s.key, dk.PrivateKeyPEM)
			sec, _ := crypto.Encrypt(s.key, "demo-webhook-secret-"+sp.slug)
			var tok string
			if sp.mode == store.DeployModeArtifact {
				tok, _ = crypto.Encrypt(s.key, "github_pat_demo_not_real")
			}
			gs, err := s.st.CreateGitSource(store.GitSource{
				Provider: "github", RepoURL: "https://github.com/" + sp.repo + ".git", CloneMethod: "deploy_key",
				PrivateKeyEnc: priv, WebhookSecretEnc: sec, DefaultBranch: sp.branch, APITokenEnc: tok,
			})
			s.check(err)
			s.check(s.st.UpdateAppGitSource(a.ID, gs.ID))
		}
	}
	for i, d := range sp.domains {
		dom, err := s.st.CreateDomain(store.Domain{AppID: a.ID, Hostname: d, TLSStatus: "pending", IsPrimary: i == 0})
		s.check(err)
		s.check(s.st.UpdateDomainTLS(dom.ID, "active", ts(s.now.Add(61*24*time.Hour))))
	}
	return appRef{id: a.ID, slug: a.Slug}
}

type dep struct {
	at      time.Time
	message string
	sha     string
	trigger string
	errText string
}

// deploys records successful deployments oldest-first; the last is the app's
// current one. Times are backdated with SQL (the store stamps "now").
func (s *seeder) deploys(a appRef, ds []dep) string {
	var last string
	for i, d := range ds {
		kind, trig := "deploy", d.trigger
		if trig == "manual" {
			kind = "manual"
		}
		dd := store.Deployment{
			AppID: a.id, Kind: kind, Status: "running", Trigger: trig, CommitSHA: fullSHA(d.sha), CommitMessage: d.message,
			ImageTag: fmt.Sprintf("deploymate/apps/%s:%s", a.slug, d.sha),
		}
		if trig == "ci" {
			dd.CIRun = 37100000000 + int64(d.at.Unix()%1000000)
			dd.CIRunNumber = 200 + i*7
		}
		x, err := s.st.CreateDeployment(dd)
		s.check(err)
		took := time.Duration(40+17*i) * time.Second
		s.stamp(x.ID, d.at, d.at.Add(took))
		last = x.ID
	}
	if last != "" {
		s.check(s.st.SetAppCurrentDeployment(a.id, last))
	}
	return last
}

func (s *seeder) failedDeploy(a appRef, at time.Time, msg, sha, errText string) string {
	x, err := s.st.CreateDeployment(store.Deployment{
		AppID: a.id, Kind: "deploy", Status: "failed", Trigger: "webhook", CommitSHA: fullSHA(sha), CommitMessage: msg, Error: errText,
	})
	s.check(err)
	s.stamp(x.ID, at, at.Add(95*time.Second))
	return x.ID
}

func fullSHA(short string) string {
	if short == "" {
		return ""
	}
	return short + strings.Repeat("0", 40-len(short))
}

func (s *seeder) stamp(deploymentID string, start, end time.Time) {
	_, err := s.st.DB().Exec(`UPDATE deployments SET created_at = ?, started_at = ?, finished_at = ? WHERE id = ?`, ts(start), ts(start), ts(end), deploymentID)
	s.check(err)
}

func (s *seeder) event(appID, kind, data string, at time.Time) {
	_, err := s.st.DB().Exec(`INSERT INTO events (ts, app_id, kind, data) VALUES (?, ?, ?, ?)`, ts(at), appID, kind, data)
	s.check(err)
}

func (s *seeder) logs(deploymentID string, lines [][2]string) {
	for _, l := range lines {
		s.check(s.st.AppendBuildLog(deploymentID, l[0], l[1]))
	}
}

func (s *seeder) envVars(a appRef, vars map[string]string) {
	for k, v := range vars {
		enc, err := crypto.Encrypt(s.key, v)
		s.check(err)
		secret := strings.Contains(k, "SECRET") || strings.Contains(k, "TOKEN") || strings.Contains(k, "PASSWORD")
		_, err = s.st.UpsertEnvVar(store.EnvVar{AppID: a.id, Key: k, ValueEnc: enc, IsSecret: secret})
		s.check(err)
	}
}

// uptime fills the last 15 minutes of probes for each of the app's domains.
func (s *seeder) uptime(a appRef) {
	ds, err := s.st.ListDomains(a.id)
	s.check(err)
	for _, d := range ds {
		for i := 30; i >= 1; i-- {
			s.check(s.st.InsertUptimeCheck(d.ID, store.UptimeCheck{
				TS: ts(s.now.Add(-time.Duration(i) * 30 * time.Second)), OK: true, StatusCode: 200, LatencyMS: int64(38 + (i*7)%31),
			}))
		}
	}
}

// ---- log text: the worker's real phrasing ------------------------------------

func webDeployLog(repo, branch, sha, msg, runtime string) [][2]string {
	return [][2]string{
		{"system", "cloning https://github.com/" + repo + " (" + branch + ")"},
		{"system", "building commit " + sha + "00000 — " + msg},
		{"system", "railpack build with runtime " + runtime},
		{"stdout", "#1 [internal] load build definition"},
		{"stdout", "#4 [install] npm ci"},
		{"stdout", "#5 [build] npm run build"},
		{"stdout", "vite v5.4.0 building for production…"},
		{"stdout", "✓ 482 modules transformed."},
		{"stdout", "dist/assets/index-9d2f1c.js   214.07 kB │ gzip: 68.30 kB"},
		{"stdout", "#7 exporting to image"},
		{"system", "starting the new container next to the old one"},
		{"system", "new container answered its health probe (HTTP 200)"},
		{"system", "switching traffic, then retiring the previous container"},
		{"system", "deployed ✓"},
	}
}

func invoicerFailureLog() [][2]string {
	return [][2]string{
		{"system", "cloning https://github.com/acme-demo/invoicer (main)"},
		{"system", "building commit 6a8b4c500000 — Switch to the new tax service"},
		{"system", "railpack build with runtime Node.js 22"},
		{"stdout", "#5 [build] npm run build"},
		{"stdout", "✓ built in 11.2s"},
		{"system", "starting the new container next to the old one"},
		{"system", "the new container failed its health probe: the container exited with code 1"},
		{"system", "── the new container's last output ──"},
		{"system", "Error: TAX_SERVICE_URL is not set"},
		{"system", "    at loadConfig (/app/server/config.js:18:11)"},
		{"system", "    at Object.<anonymous> (/app/server/index.js:4:16)"},
		{"system", "── end of container output ──"},
		{"system", "new container failed its health probe — the previous container is still serving"},
	}
}
