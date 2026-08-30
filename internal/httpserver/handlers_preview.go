package httpserver

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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
		// SPAs emit absolute-path asset URLs (src="/assets/…", href="/…")
		// that resolve to the dashboard root and 404 outside the preview
		// prefix (a <base> tag cannot help — absolute paths replace the
		// base's path). Rewrite them to carry the prefix; the proxy strips
		// it on the way in. Subdomain routes already reach the app at "/"
		// and need no rewriting. (fetch("/api/…") inside JS bundles can't
		// be rewritten here — apps should use relative URLs or a domain.)
		if strings.HasPrefix(r.URL.Path, "/preview/"+app.Slug) &&
			strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				resp.Body.Close()
				return err
			}
			resp.Body.Close()
			body = rewritePreviewURLs(body, app.Slug)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		}
		return nil
	}
	proxy.ServeHTTP(w, r)
}

// rewritePreviewURLs prefixes absolute-path src/href values with the
// app's preview path so assets resolve through the dashboard proxy:
// src="/assets/app.js" → src="/preview/{slug}/assets/app.js". Protocol-
// relative (//host), fragment-only, and already-prefixed URLs are left
// alone. Both quote styles are handled; srcset and inline CSS url() are
// not (out of scope for now).
func rewritePreviewURLs(body []byte, slug string) []byte {
	prefix := "/preview/" + slug + "/"
	re := regexp.MustCompile(`(src|href)=(["'])/([^"']*)`)
	return re.ReplaceAllFunc(body, func(m []byte) []byte {
		p := re.FindSubmatch(m)
		attr, quote, rest := p[1], p[2], p[3]
		if bytes.HasPrefix(rest, []byte("/")) || // protocol-relative //host
			bytes.HasPrefix(rest, []byte("preview/"+slug+"/")) { // already prefixed
			return m
		}
		out := append([]byte{}, attr...)
		out = append(out, '=')
		out = append(out, quote...)
		out = append(out, prefix...)
		return append(out, rest...)
	})
}

// handlePreview reverse-proxies /preview/{slug}/* (session-protected).
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	// Canonicalize to a trailing slash: the document URL's directory is
	// the app's root, so relative asset URLs (./assets/…) must resolve
	// under /preview/{slug}/, not /preview/.
	if r.URL.Path == "/preview/"+app.Slug {
		http.Redirect(w, r, "/preview/"+app.Slug+"/", http.StatusTemporaryRedirect)
		return
	}
	s.proxyToApp(w, r, app)
}

// previewURL builds the absolute preview URL: a public subdomain when
// PreviewHost is configured, otherwise the dashboard's /preview path.
func (s *Server) previewURL(r *http.Request, app store.App) string {
	if s.previewHost != "" {
		// Plain http for now: Cloudflare's free plan issues no edge cert for
		// the wildcard, so https fails until each {slug} has its own DNS
		// record (docs/specs/cloudflare-tunnel.md). Flip back to https://
		// when per-app records exist (auto-DNS, docs/improvements.md).
		return "http://" + app.Slug + "." + s.previewHost
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/preview/" + app.Slug
}
