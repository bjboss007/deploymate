package httpserver

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// previewForSlug reverse-proxies to the app's loopback port. Shared by the
// dashboard's /preview/{slug} (session-protected) and the public
// {slug}.{previewHost} subdomain route.
func (s *Server) previewForSlug(w http.ResponseWriter, r *http.Request, slug string) {
	app, err := s.store.GetAppBySlug(slug)
	if err != nil {
		http.Error(w, "no app with that name", http.StatusNotFound)
		return
	}
	s.proxyToApp(w, r, app)
}

// proxyToApp reverse-proxies one request to an app's loopback port. The
// Go reverse proxy forwards WebSocket upgrades natively.
func (s *Server) proxyToApp(w http.ResponseWriter, r *http.Request, app store.App) {
	if app.Status != "running" {
		http.Error(w, "app is not running — deploy or start it first", http.StatusServiceUnavailable)
		return
	}
	if app.Port <= 0 {
		http.Error(w, "no routing port set for this app — set one on the app page (Domains panel)", http.StatusBadRequest)
		return
	}

	// The app's container publishes its port on 127.0.0.1 only — reachable
	// from this host process on Linux servers AND Docker Desktop, and never
	// from the internet.
	target, err := url.Parse("http://127.0.0.1:" + strconv.Itoa(runtime.PreviewPort(app.Slug)))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// The app must see the path as it was requested, minus our
		// /preview/{slug} prefix (subdomain routes have no prefix).
		if strings.HasPrefix(req.URL.Path, "/preview/"+app.Slug) {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/preview/"+app.Slug)
			if req.URL.Path == "" {
				req.URL.Path = "/"
			}
		}
		req.Host = target.Host
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		slog.Error("preview: proxy", "app", app.Slug, "err", err)
		http.Error(w, "preview proxy error: "+err.Error(), http.StatusBadGateway)
	}
	// Apps that redirect with absolute URLs back to their loopback port
	// (Spring Security logins, trailing-slash redirects) must stay inside
	// the preview path.
	loopbackPrefix := "http://127.0.0.1:" + strconv.Itoa(runtime.PreviewPort(app.Slug))
	proxy.ModifyResponse = func(resp *http.Response) error {
		if loc := resp.Header.Get("Location"); strings.HasPrefix(loc, loopbackPrefix) {
			resp.Header.Set("Location", "/preview/"+app.Slug+strings.TrimPrefix(loc, loopbackPrefix))
		}
		// SPAs emit absolute asset URLs (src="/assets/…", fetch("/api/…"))
		// that resolve to the dashboard root and 404 outside the preview
		// prefix. A <base> tag points every absolute URL at the app's own
		// root, which the proxy strips and forwards. Subdomain routes
		// already reach the app at "/" and need no base.
		if strings.HasPrefix(r.URL.Path, "/preview/"+app.Slug) &&
			strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				resp.Body.Close()
				return err
			}
			resp.Body.Close()
			body = bytes.Replace(body, []byte("<head>"),
				[]byte(`<head><base href="/preview/`+app.Slug+`/">`), 1)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}

// handlePreview reverse-proxies /preview/{slug}/* (session-protected).
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	s.proxyToApp(w, r, app)
}

// previewURL builds the absolute preview URL: a public subdomain when
// PreviewHost is configured, otherwise the dashboard's /preview path.
func (s *Server) previewURL(r *http.Request, app store.App) string {
	if s.previewHost != "" {
		return "https://" + app.Slug + "." + s.previewHost
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/preview/" + app.Slug
}
