package httpserver

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
)

// Per-token limits. Reads are generous (an agent polling status is normal);
// writes are tight, because a looping agent that deploys 200 times is an
// incident. Counted per token, in memory — a restart forgives, which is fine.
const (
	readPerMinute   = 300
	writePerMinute  = 20
	writePerHour    = 200
	limiterPruneGap = 10 * time.Minute
)

type tokenWindow struct {
	reads  []time.Time
	writes []time.Time
}

type apiLimiter struct {
	mu        sync.Mutex
	byToken   map[string]*tokenWindow
	lastPrune time.Time
	now       func() time.Time // tests set it
}

func newAPILimiter() *apiLimiter {
	return &apiLimiter{byToken: map[string]*tokenWindow{}, now: time.Now}
}

// keepAfter drops timestamps at or before cutoff.
func keepAfter(ts []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cutoff) {
		i++
	}
	return ts[i:]
}

// allow records one request for the token and reports whether it is within the
// limits; when it is not, how long until it would be.
func (l *apiLimiter) allow(tokenID string, write bool) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastPrune) > limiterPruneGap {
		for id, w := range l.byToken {
			if len(keepAfter(w.writes, now.Add(-time.Hour))) == 0 && len(keepAfter(w.reads, now.Add(-time.Minute))) == 0 {
				delete(l.byToken, id)
			}
		}
		l.lastPrune = now
	}
	w := l.byToken[tokenID]
	if w == nil {
		w = &tokenWindow{}
		l.byToken[tokenID] = w
	}
	w.reads = keepAfter(w.reads, now.Add(-time.Minute))
	w.writes = keepAfter(w.writes, now.Add(-time.Hour))
	if !write {
		if len(w.reads) >= readPerMinute {
			return false, w.reads[0].Add(time.Minute).Sub(now)
		}
		w.reads = append(w.reads, now)
		return true, 0
	}
	inMinute := keepAfter(w.writes, now.Add(-time.Minute))
	if len(inMinute) >= writePerMinute {
		return false, inMinute[0].Add(time.Minute).Sub(now)
	}
	if len(w.writes) >= writePerHour {
		return false, w.writes[0].Add(time.Hour).Sub(now)
	}
	w.writes = append(w.writes, now)
	return true, 0
}

// limit is the middleware: it runs after RequireAPIToken.
func (l *apiLimiter) limit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := auth.TokenFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		write := r.Method != http.MethodGet && r.Method != http.MethodHead
		if allowed, wait := l.allow(tok.ID, write); !allowed {
			secs := int(wait.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			apiErr(w, http.StatusTooManyRequests, "too many requests from this token — slow down and retry in "+strconv.Itoa(secs)+" s")
			return
		}
		next.ServeHTTP(w, r)
	})
}
