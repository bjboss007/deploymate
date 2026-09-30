package httpserver

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/habibmuhammad/deploymate/internal/appspec"
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

	// The app's containers publish their port on 127.0.0.1 only —
	// reachable from this host process on Linux servers AND Docker
	// Desktop, and never from the internet. With replicas this is the
	// dashboard-side load balancer: one preview URL, round-robin over the
	// slots (unhealthy ones last), failing over to the next slot on a dial
	// error (see failoverTransport).
	hosts := s.previewHosts(app)
	target, err := url.Parse("http://" + hosts[0])
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &failoverTransport{base: http.DefaultTransport, hosts: hosts}
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
	// the preview path — whichever replica answered.
	proxy.ModifyResponse = func(resp *http.Response) error {
		loc := resp.Header.Get("Location")
		for _, h := range hosts {
			if loopbackPrefix := "http://" + h; strings.HasPrefix(loc, loopbackPrefix) {
				resp.Header.Set("Location", "/preview/"+app.Slug+strings.TrimPrefix(loc, loopbackPrefix))
				break
			}
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

// previewHosts orders the app's replica loopback addresses for one request:
// the start rotates per request (round-robin), slots the monitor last saw
// unhealthy go to the back (skipped while any other slot is up, still tried
// when every slot is down), and port-less slots are dropped.
func (s *Server) previewHosts(app store.App) []string {
	slots := s.appSlots(app)
	v, _ := s.previewRR.LoadOrStore(app.ID, new(atomic.Uint64))
	start := int(v.(*atomic.Uint64).Add(1)-1) % len(slots)
	var healthy, sick []string
	for i := range slots {
		sl := slots[(start+i)%len(slots)]
		if sl.HostPort <= 0 {
			continue
		}
		h := "127.0.0.1:" + strconv.Itoa(sl.HostPort)
		if sl.Status == "unhealthy" {
			sick = append(sick, h)
		} else {
			healthy = append(healthy, h)
		}
	}
	hosts := append(healthy, sick...)
	if len(hosts) == 0 {
		// No recorded port at all: the pre-replicas resolution.
		hosts = []string{"127.0.0.1:" + strconv.Itoa(appspec.ResolvedPreviewPort(app))}
	}
	return hosts
}

// failoverTransport sends a proxied request to the first replica and, when
// the connection cannot even be dialed (container restarting, killed,
// between swaps), retries the next one. Only dial errors fail over — the
// request never reached an app, so a retry cannot double-apply it — and a
// request body is only replayed when it can be re-read.
type failoverTransport struct {
	base  http.RoundTripper
	hosts []string
}

func (t *failoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var lastErr error
	for i, h := range t.hosts {
		r := req
		if i > 0 {
			if req.Body != nil && req.Body != http.NoBody {
				if req.GetBody == nil {
					break // body already consumed; cannot replay safely
				}
				body, err := req.GetBody()
				if err != nil {
					break
				}
				r = req.Clone(req.Context())
				r.Body = body
			} else {
				r = req.Clone(req.Context())
			}
		}
		r.URL.Host, r.Host = h, h
		resp, err := t.base.RoundTrip(r)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		var opErr *net.OpError
		if !errors.As(err, &opErr) || opErr.Op != "dial" {
			return nil, err
		}
	}
	return nil, lastErr
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
		// Plain http for now: auto-DNS creates the per-app record at app
		// creation (internal/dns), but Cloudflare's edge cert still lags —
		// first issuance can take minutes to hours. Browsers auto-upgrade
		// to https, which works once the cert lands. Flip to https:// when
		// issuance is confirmed reliable (docs/improvements.md).
		return "http://" + app.Slug + "." + s.previewHost
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/preview/" + app.Slug
}
