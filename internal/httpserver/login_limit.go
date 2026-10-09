package httpserver

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Failed sign-ins are counted per client address and, more loosely, per email, so
// a dashboard on the public internet cannot be guessed at. The email limit is high
// on purpose: a low one would let anyone lock the owner out by failing on purpose.
const (
	loginWindow       = 15 * time.Minute
	loginMaxFailsIP   = 10
	loginMaxFailsMail = 30
)

type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
	now   func() time.Time // tests set it
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{fails: map[string][]time.Time{}, now: time.Now}
}

// blocked reports whether either key is over its limit, and for how long.
func (l *loginLimiter) blocked(ip, email string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	var wait time.Duration
	for key, max := range map[string]int{"ip:" + ip: loginMaxFailsIP, "mail:" + strings.ToLower(email): loginMaxFailsMail} {
		recent := keepAfter(l.fails[key], now.Add(-loginWindow))
		l.fails[key] = recent
		if len(recent) >= max {
			if w := recent[0].Add(loginWindow).Sub(now); w > wait {
				wait = w
			}
		}
	}
	return wait > 0, wait
}

// fail records a failed sign-in.
func (l *loginLimiter) fail(ip, email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, key := range []string{"ip:" + ip, "mail:" + strings.ToLower(email)} {
		l.fails[key] = append(keepAfter(l.fails[key], now.Add(-loginWindow)), now)
	}
	// Keep the map from growing without bound under a scan.
	if len(l.fails) > 5000 {
		for k, v := range l.fails {
			if len(keepAfter(v, now.Add(-loginWindow))) == 0 {
				delete(l.fails, k)
			}
		}
	}
}

// succeed forgives the address (the owner typed it right, eventually).
func (l *loginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, "ip:"+ip)
}

// clientIP is the address a sign-in attempt came from. Behind Traefik (or a tunnel)
// every request arrives from loopback, so the real client is the LAST entry of
// X-Forwarded-For — the one our own proxy appended. Earlier entries are whatever the
// client chose to send and are never trusted. From a non-loopback peer, the header
// is ignored altogether.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}
