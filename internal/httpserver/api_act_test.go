package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// lifeRT records container lifecycle calls.
type lifeRT struct {
	runtime.Runtime
	mu    sync.Mutex
	calls []string
}

func (l *lifeRT) rec(s string)                                  { l.mu.Lock(); l.calls = append(l.calls, s); l.mu.Unlock() }
func (l *lifeRT) Start(_ context.Context, n string) error       { l.rec("start " + n); return nil }
func (l *lifeRT) Stop(_ context.Context, n string, _ int) error { l.rec("stop " + n); return nil }
func (l *lifeRT) Inspect(context.Context, string) (runtime.Info, error) {
	return runtime.Info{Running: true, PublishedPorts: []string{"8080/tcp"}}, nil
}

func (e *prebuiltEnv) apiPost(t *testing.T, path, token string) (int, map[string]any) {
	t.Helper()
	code, body := e.api(t, http.MethodPost, path, token)
	var m map[string]any
	if strings.HasPrefix(strings.TrimSpace(body), "{") {
		m = decode(t, body)
	}
	return code, m
}

// A read token cannot act; a deploy token can, and every action lands in the
// audit log and the app's own history, naming the token.
func TestAPIDeployTierScopeAndAudit(t *testing.T) {
	e := newPrebuiltEnv(t)
	reader := e.createToken(t, "watcher", "read", "90")
	deployer := e.createToken(t, "agent", "deploy", "90")

	// A failed deployment to retry, and a running one to roll back to / redeploy.
	good, _ := e.st.CreateDeployment(store.Deployment{AppID: e.app.ID, Kind: "deploy", Status: "running", ImageTag: "img:good", CommitSHA: "aaa"})
	e.st.SetAppCurrentDeployment(e.app.ID, good.ID)
	time.Sleep(1100 * time.Millisecond)
	failed, _ := e.st.CreateDeployment(store.Deployment{AppID: e.app.ID, Kind: "deploy", Status: "failed", CommitSHA: "bbb", Error: "boom"})

	if code, _ := e.apiPost(t, "/api/v1/deployments/"+failed.ID+"/retry", reader); code != http.StatusForbidden {
		t.Fatalf("a read token retried a deployment (%d)", code)
	}
	if ds, _ := e.st.ListDeployments(e.app.ID, 10); len(ds) != 2 {
		t.Fatalf("a refused call changed state (%d deployments)", len(ds))
	}

	code, res := e.apiPost(t, "/api/v1/deployments/"+failed.ID+"/retry", deployer)
	if code != http.StatusAccepted || res["deployment_id"] == "" || res["status"] != "queued" {
		t.Fatalf("retry = %d %v", code, res)
	}
	code, res = e.apiPost(t, "/api/v1/apps/api/redeploy", deployer)
	if code != http.StatusAccepted {
		t.Fatalf("redeploy = %d %v", code, res)
	}
	code, res = e.apiPost(t, "/api/v1/deployments/"+good.ID+"/rollback", deployer)
	if code != http.StatusAccepted {
		t.Fatalf("rollback = %d %v", code, res)
	}
	// A deploy of a git app with no CI: queues a build.
	code, res = e.apiPost(t, "/api/v1/apps/api/deploy", deployer)
	if code != http.StatusAccepted {
		t.Fatalf("deploy = %d %v", code, res)
	}
	// Refusals are 409 with the reason, and are audited as refused.
	code, res = e.apiPost(t, "/api/v1/deployments/"+good.ID+"/retry", deployer)
	if code != http.StatusConflict || !strings.Contains(res["error"].(string), "Only a failed") {
		t.Errorf("retry of a running deployment = %d %v", code, res)
	}

	entries, _ := e.st.ListAudit(50)
	var ok, refused int
	for _, a := range entries {
		if a.TokenName != "agent" {
			t.Errorf("audit entry from %q; the read token must leave none", a.TokenName)
		}
		if a.Result == "ok" {
			ok++
		} else {
			refused++
		}
	}
	if ok != 4 || refused != 1 {
		t.Errorf("audit: %d ok / %d refused, want 4 / 1: %+v", ok, refused, entries)
	}
	evs, _ := e.st.ListEvents(e.app.ID, 20)
	found := false
	for _, ev := range evs {
		if ev.Kind == store.EventAPIAction && strings.Contains(ev.Data, "agent") {
			found = true
		}
	}
	if !found {
		t.Error("the app's history should record the API action with the token's name")
	}
	// The tokens page lists what was done.
	req := httptest.NewRequest(http.MethodGet, "/settings/tokens", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if body := rec.Body.String(); !strings.Contains(body, "What tokens did") || !strings.Contains(body, "retry") || !strings.Contains(body, "refused") {
		t.Error("the tokens page should show the audit trail")
	}
}

func TestAPILifecycleActions(t *testing.T) {
	e := newPrebuiltEnv(t)
	rt := &lifeRT{}
	e.s.rt = rt
	tok := e.createToken(t, "agent", "deploy", "90")
	for _, step := range []struct{ action, want string }{{"restart", "running"}, {"stop", "stopped"}, {"start", "running"}} {
		action, want := step.action, step.want
		code, res := e.apiPost(t, "/api/v1/apps/api/"+action, tok)
		if code != http.StatusOK || res["status"] != want {
			t.Fatalf("%s = %d %v", action, code, res)
		}
	}
	app, _ := e.reload(t)
	if app.Status != "running" {
		t.Errorf("app status after restart/stop/start = %q", app.Status)
	}
	if len(rt.calls) < 4 {
		t.Errorf("runtime calls = %v", rt.calls)
	}
	evs, _ := e.st.ListEvents(e.app.ID, 20)
	via := 0
	for _, ev := range evs {
		if strings.Contains(ev.Data, "the API (token “agent”)") {
			via++
		}
	}
	if via != 3 {
		t.Errorf("app history names the token on %d of 3 lifecycle events", via)
	}
}

// Writes are limited per token; reads are not blocked by write limits.
func TestAPIRateLimits(t *testing.T) {
	l := newAPILimiter()
	now := time.Now()
	l.now = func() time.Time { return now }
	for i := 0; i < writePerMinute; i++ {
		if ok, _ := l.allow("t1", true); !ok {
			t.Fatalf("write %d refused too early", i)
		}
	}
	ok, wait := l.allow("t1", true)
	if ok || wait <= 0 || wait > time.Minute {
		t.Errorf("write beyond the per-minute limit: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("t2", true); !ok {
		t.Error("another token must have its own budget")
	}
	if ok, _ := l.allow("t1", false); !ok {
		t.Error("reads are not charged to the write budget")
	}
	now = now.Add(61 * time.Second)
	if ok, _ := l.allow("t1", true); !ok {
		t.Error("the minute window should have slid")
	}
	// The hourly cap.
	l2 := newAPILimiter()
	t0 := time.Now()
	l2.now = func() time.Time { return t0 }
	for i := 0; i < writePerHour; i++ {
		t0 = t0.Add(4 * time.Second) // under the per-minute limit, over an hour in total
		if ok, _ := l2.allow("t", true); !ok {
			t.Fatalf("write %d refused", i)
		}
	}
	if ok, _ := l2.allow("t", true); ok {
		t.Error("the hourly cap did not hold")
	}
	for i := 0; i < readPerMinute; i++ {
		l.allow("r", false)
	}
	if ok, _ := l.allow("r", false); ok {
		t.Error("the read limit did not hold")
	}
}

// Over the limit the API answers 429 with Retry-After.
func TestAPIRateLimitResponse(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "deploy", "90")
	h := e.s.Handler()
	e.s.apiRate.now = time.Now
	var last *httptest.ResponseRecorder
	for i := 0; i < writePerMinute+1; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/apps/nope/redeploy", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		last = httptest.NewRecorder()
		h.ServeHTTP(last, req)
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" {
		t.Errorf("21st write = %d, Retry-After %q", last.Code, last.Header().Get("Retry-After"))
	}
}
