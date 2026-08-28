package httpserver

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// handlePreview reverse-proxies /preview/{slug}/* to the app's container on
// the private network — an access URL before any real domain is attached.
// The Go reverse proxy forwards WebSocket upgrades natively.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
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
		// /preview/{slug} prefix.
		req.URL.Path = "/" + chi.URLParam(req, "*")
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
		return nil
	}
	proxy.ServeHTTP(w, r)
}

// previewURL builds the absolute preview URL for the current request host,
// so it stays correct behind Traefik on the server and on localhost in dev.
func previewURL(r *http.Request, app store.App) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/preview/" + app.Slug
}
