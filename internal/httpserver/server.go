// Package httpserver wires the chi router: static assets, public webhook
// and auth routes, and the authenticated dashboard.
package httpserver

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
	"github.com/habibmuhammad/deploymate/internal/webhooks"
	"github.com/habibmuhammad/deploymate/web"
	"strings"
)

// Server holds handler dependencies.
type Server struct {
	store       *store.Store
	rt          runtime.Runtime
	events      *sse.Broker
	encKey      [32]byte
	deliveries  *webhooks.DeliveryCache
	leMode      string
	previewHost string
}

// New builds a Server.
func New(st *store.Store, rt runtime.Runtime, events *sse.Broker, encKey [32]byte, leMode, previewHost string) *Server {
	return &Server{
		store: st, rt: rt, events: events, encKey: encKey, leMode: leMode, previewHost: previewHost,
		deliveries: webhooks.NewDeliveryCache(),
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
		r.Post("/alerts", am.CheckCSRF(s.handleAlertCreate))
		r.Post("/alerts/{id}/delete", am.CheckCSRF(s.handleAlertDelete))
		r.Get("/projects/{slug}", s.handleProjectDetail)
		r.Post("/projects/{slug}/delete", am.CheckCSRF(s.handleProjectDelete))
		r.Post("/projects/{slug}/apps", am.CheckCSRF(s.handleAppCreate))
		r.Post("/projects/{slug}/services", am.CheckCSRF(s.handleServiceCreate))
		r.Get("/apps/{slug}", s.handleAppPage)
		r.Get("/apps/{slug}/logs", s.handleAppLogs)
		r.Get("/preview/{slug}", s.handlePreview)
		r.Get("/preview/{slug}/*", s.handlePreview)
		r.Get("/apps/{slug}/metrics", s.handleAppMetricsJSON)
		r.Get("/apps/{slug}/uptime", s.handleAppUptimeJSON)
		r.Post("/apps/{slug}/deploy", am.CheckCSRF(s.handleAppDeploy))
		r.Post("/apps/{slug}/stop", am.CheckCSRF(s.handleAppStop))
		r.Post("/apps/{slug}/start", am.CheckCSRF(s.handleAppStart))
		r.Post("/apps/{slug}/delete", am.CheckCSRF(s.handleAppDelete))
		r.Post("/apps/{slug}/env", am.CheckCSRF(s.handleEnvVarCreate))
		r.Post("/apps/{slug}/env/{id}/delete", am.CheckCSRF(s.handleEnvVarDelete))
		r.Post("/apps/{slug}/git", am.CheckCSRF(s.handleGitConnect))
		r.Post("/apps/{slug}/git/deploy", am.CheckCSRF(s.handleGitDeploy))
		r.Post("/apps/{slug}/domains", am.CheckCSRF(s.handleDomainCreate))
		r.Post("/apps/{slug}/domains/{id}/delete", am.CheckCSRF(s.handleDomainDelete))
		r.Post("/apps/{slug}/port", am.CheckCSRF(s.handleAppPort))
		r.Post("/apps/{slug}/runtime", am.CheckCSRF(s.handleAppRuntime))
		r.Get("/deployments/{id}", s.handleDeploymentPage)
		r.Get("/deployments/{id}/stream", s.handleDeploymentStream)
		r.Post("/deployments/{id}/rollback", am.CheckCSRF(s.handleRollback))
		r.Get("/services/{slug}", s.handleServicePage)
		r.Post("/services/{slug}/start", am.CheckCSRF(s.handleServiceStart))
		r.Post("/services/{slug}/stop", am.CheckCSRF(s.handleServiceStop))
		r.Post("/services/{slug}/delete", am.CheckCSRF(s.handleServiceDelete))
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
