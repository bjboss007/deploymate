// Package httpserver wires the chi router: static assets, public webhook
// and auth routes, and the authenticated dashboard.
package httpserver

import (
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/backup"
	"github.com/habibmuhammad/deploymate/internal/dns"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
	"github.com/habibmuhammad/deploymate/web"
)

// Server holds handler dependencies.
type Server struct {
	store       *store.Store
	rt          runtime.Runtime
	prov        *services.Provisioner
	events      *sse.Broker
	encKey      [32]byte
	deliveries  *webhooks.DeliveryCache
	leMode      string
	previewHost string
	dataDir     string       // repo mirrors for the deploy-review page
	dns         dns.Manager  // nil disables auto-DNS
	backups     *backup.Manager // nil disables the backups UI/actions
	previewRR   sync.Map        // appID -> *atomic.Uint64: /preview round-robin cursor over replicas
}

// New builds a Server.
func New(st *store.Store, rt runtime.Runtime, prov *services.Provisioner, events *sse.Broker, encKey [32]byte, leMode, previewHost, dataDir string, dnsManager dns.Manager, backupMgr *backup.Manager) *Server {
	return &Server{
		store: st, rt: rt, prov: prov, events: events, encKey: encKey, leMode: leMode, previewHost: previewHost,
		dataDir:    dataDir,
		deliveries: webhooks.NewDeliveryCache(),
		dns:        dnsManager,
		backups:    backupMgr,
	}
}

// Handler assembles the full route tree.
func (s *Server) Handler() http.Handler {
	am := &auth.Middleware{Store: s.store}

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(requestLog)
	// Public app subdomains: {slug}.{previewHost} routes straight to the
	// app, no session (that's the point of a public preview URL). The
	// reverse proxy must preserve the Host header (cloudflared does).
	if s.previewHost != "" {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				sub, ok := strings.CutSuffix(req.Host, "."+s.previewHost)
				if ok && sub != "" && !strings.Contains(sub, ".") {
					s.previewForSlug(w, req, sub)
					return
				}
				next.ServeHTTP(w, req)
			})
		})
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	staticFS, err := fs.Sub(web.StaticFS, "static")
	if err != nil {
		panic("httpserver: static embed broken: " + err.Error())
	}
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Public, session-aware routes.
	r.Group(func(r chi.Router) {
		r.Use(am.LoadSession)
		r.Get("/login", s.handleLoginPage)
		r.Post("/login", s.handleLogin)
		r.Post("/logout", am.CheckCSRF(s.handleLogout))
	})

	// Git provider webhooks: public, authenticated by their secret instead.
	r.Post("/hooks/{id}", s.handleWebhook)

	// Dashboard.
	r.Group(func(r chi.Router) {
		r.Use(am.LoadSession)
		r.Use(am.RequireUser)
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/projects", http.StatusSeeOther)
		})
		r.Get("/projects", s.handleProjectsList)
		r.Post("/projects", am.CheckCSRF(s.handleProjectCreate))
		r.Get("/alerts", s.handleAlertsPage)
		r.Get("/stats", s.handleStatsPage)
		r.Post("/alerts", am.CheckCSRF(s.handleAlertCreate))
		r.Post("/alerts/{id}/delete", am.CheckCSRF(s.handleAlertDelete))
		r.Get("/projects/{slug}", s.handleProjectDetail)
		r.Post("/projects/{slug}/delete", am.CheckCSRF(s.handleProjectDelete))
		r.Post("/projects/{slug}/apps", am.CheckCSRF(s.handleAppCreate))
		r.Post("/projects/{slug}/services", am.CheckCSRF(s.handleServiceCreate))
		r.Get("/apps/{slug}", s.handleAppPage)
		r.Get("/apps/{slug}/history", s.handleAppHistory)
		r.Get("/apps/{slug}/releases", s.handleAppReleases)
		r.Get("/apps/{slug}/deploy-preview", s.handleDeployPreview)
		r.Get("/apps/{slug}/logs", s.handleAppLogs)
		r.Get("/preview/{slug}", s.handlePreview)
		r.Get("/preview/{slug}/*", s.handlePreview)
		r.Get("/apps/{slug}/metrics", s.handleAppMetricsJSON)
		r.Get("/apps/{slug}/uptime", s.handleAppUptimeJSON)
		r.Post("/apps/{slug}/deploy", am.CheckCSRF(s.handleAppDeploy))
		r.Post("/apps/{slug}/stop", am.CheckCSRF(s.handleAppStop))
		r.Post("/apps/{slug}/start", am.CheckCSRF(s.handleAppStart))
		r.Post("/apps/{slug}/restart", am.CheckCSRF(s.handleAppRestart))
		r.Post("/apps/{slug}/replicas", am.CheckCSRF(s.handleAppReplicas))
		r.Post("/apps/{slug}/health-path", am.CheckCSRF(s.handleAppHealthPath))
		r.Post("/apps/{slug}/delete", am.CheckCSRF(s.handleAppDelete))
		r.Post("/apps/{slug}/env", am.CheckCSRF(s.handleEnvVarCreate))
		r.Post("/apps/{slug}/env/{id}/delete", am.CheckCSRF(s.handleEnvVarDelete))
		r.Post("/apps/{slug}/git", am.CheckCSRF(s.handleGitConnect))
		r.Post("/apps/{slug}/git/deploy", am.CheckCSRF(s.handleGitDeploy))
		r.Post("/apps/{slug}/domains", am.CheckCSRF(s.handleDomainCreate))
		r.Post("/apps/{slug}/domains/{id}/delete", am.CheckCSRF(s.handleDomainDelete))
		r.Post("/apps/{slug}/port", am.CheckCSRF(s.handleAppPort))
		r.Post("/apps/{slug}/runtime", am.CheckCSRF(s.handleAppRuntime))
		r.Post("/apps/{slug}/environment", am.CheckCSRF(s.handleAppEnvironment))
		r.Get("/deployments/{id}", s.handleDeploymentPage)
		r.Get("/deployments/{id}/stream", s.handleDeploymentStream)
		r.Post("/deployments/{id}/rollback", am.CheckCSRF(s.handleRollback))
		r.Get("/services/{slug}", s.handleServicePage)
		r.Post("/services/{slug}/start", am.CheckCSRF(s.handleServiceStart))
		r.Post("/services/{slug}/stop", am.CheckCSRF(s.handleServiceStop))
		r.Post("/services/{slug}/restart", am.CheckCSRF(s.handleServiceRestart))
		r.Post("/services/{slug}/keep", am.CheckCSRF(s.handleServiceKeep))
		r.Post("/services/{slug}/delete", am.CheckCSRF(s.handleServiceDelete))
		r.Post("/services/{slug}/backup", am.CheckCSRF(s.handleBackupConfigSave))
		r.Post("/services/{slug}/backup/now", am.CheckCSRF(s.handleBackupNow))
		r.Get("/services/{slug}/backup/key", s.handleBackupKey)
		r.Post("/services/{slug}/backups/restore", am.CheckCSRF(s.handleBackupRestore))
	})

	return r
}

func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"dur", time.Since(start).Round(time.Microsecond).String(),
		)
	})
}
