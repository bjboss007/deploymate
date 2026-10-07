package httpserver

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The session cookie is Secure whenever the visitor is on HTTPS — including when a
// local proxy (cloudflared, Traefik) ended TLS — but a client far away cannot make it
// believe so with a forged header.
func TestIsHTTPSBehindAProxy(t *testing.T) {
	mk := func(remote, proto string, withTLS bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		if withTLS {
			r.TLS = &tls.ConnectionState{}
		}
		return r
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"direct TLS", mk("203.0.113.9:5000", "", true), true},
		{"plain http, no proxy", mk("127.0.0.1:5000", "", false), false},
		{"tunnel on loopback says https", mk("127.0.0.1:5000", "https", false), true},
		{"ipv6 loopback says https", mk("[::1]:5000", "HTTPS", false), true},
		{"loopback says http", mk("127.0.0.1:5000", "http", false), false},
		{"remote client forges https", mk("203.0.113.9:5000", "https", false), false},
		{"garbage remote address", mk("not-an-address", "https", false), false},
	} {
		if got := isHTTPS(tc.r); got != tc.want {
			t.Errorf("%s: isHTTPS = %v, want %v", tc.name, got, tc.want)
		}
	}
}
