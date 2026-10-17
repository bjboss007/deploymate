package httpserver

import (
	"net/http"
	"strings"
)

// Dashboard pages still use a few inline scripts and handlers, so script/style keep 'unsafe-inline';
// the policy's value is framing, <base>, plugins and where requests may go.
const dashboardCSP = "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// securityHeaders hardens the dashboard's own responses. /preview/ proxies someone's app, whose headers
// are theirs to choose, so it is left alone.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/preview/") {
			h := w.Header()
			h.Set("Content-Security-Policy", dashboardCSP)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "same-origin")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			if isHTTPS(r) {
				h.Set("Strict-Transport-Security", "max-age=31536000")
			}
		}
		next.ServeHTTP(w, r)
	})
}

// staticCache makes versioned asset URLs (?v=release) cacheable for a year, and everything else revalidate on
// every use. Fonts keep their weight in the file name, so they never change under one name.
func staticCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" || strings.HasPrefix(r.URL.Path, "/static/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}
