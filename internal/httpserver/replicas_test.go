package httpserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/pkg/stdcopy"

	"github.com/habibmuhammad/deploymate/internal/appspec"
	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// replicaTestEnv is an app with a logged-in owner session ("tok"/"csrf").
func replicaTestEnv(t *testing.T, app store.App) (*store.Store, store.App) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	owner, err := st.CreateUser(store.User{Email: "owner@test.dev", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := st.CreateProject(store.Project{UserID: owner.ID, Name: "Test", Slug: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession(store.Session{
		UserID: owner.ID, TokenHash: auth.HashToken("tok"), CSRFToken: "csrf",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	app.ProjectID = proj.ID
	app, err = st.CreateApp(app)
	if err != nil {
		t.Fatal(err)
	}
	return st, app
}

// namedBackend serves its name on every path.
func namedBackend(t *testing.T, name string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, name)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return port
}

func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func previewHits(t *testing.T, s *Server, app store.App, n int) map[string]int {
	t.Helper()
	seen := map[string]int{}
	for i := 0; i < n; i++ {
		rec := httptest.NewRecorder()
		s.proxyToApp(rec, httptest.NewRequest("GET", "/preview/"+app.Slug+"/", nil), app)
		if rec.Code != http.StatusOK {
			t.Fatalf("hit %d: status %d: %s", i, rec.Code, rec.Body.String())
		}
		seen[rec.Body.String()]++
	}
	return seen
}

func setSlots(t *testing.T, st *store.Store, app store.App, ports []int, statuses []string) {
	t.Helper()
	if err := st.DeleteAppReplicasAbove(app.ID, 0); err != nil {
		t.Fatal(err)
	}
	for i, p := range ports {
		slot := i + 1
		if err := st.UpsertAppReplica(store.AppReplica{AppID: app.ID, Slot: slot, ContainerName: appspec.SlotName(app.Slug, slot), HostPort: p}); err != nil {
			t.Fatal(err)
		}
		if statuses[i] != "" {
			if err := st.SetAppReplicaStatus(app.ID, slot, statuses[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestPreviewLoadBalancesReplicas: one preview URL, round-robin across the
// replicas; a slot the monitor marked unhealthy is skipped while another is
// up; a slot whose port refuses connections fails over to the next one;
// and when every slot is marked unhealthy they are all still tried.
func TestPreviewLoadBalancesReplicas(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "LB", Slug: "lb", Status: "running", Port: 8080, Image: "x:1"})
	s := &Server{store: st}
	a, b := namedBackend(t, "A"), namedBackend(t, "B")

	setSlots(t, st, app, []int{a, b}, []string{"", ""})
	if seen := previewHits(t, s, app, 6); seen["A"] != 3 || seen["B"] != 3 {
		t.Errorf("round-robin = %v, want A:3 B:3", seen)
	}

	setSlots(t, st, app, []int{a, b}, []string{"healthy", "unhealthy"})
	if seen := previewHits(t, s, app, 4); seen["A"] != 4 {
		t.Errorf("unhealthy slot not skipped: %v", seen)
	}

	setSlots(t, st, app, []int{a, closedPort(t)}, []string{"", ""})
	if seen := previewHits(t, s, app, 4); seen["A"] != 4 {
		t.Errorf("dial failure did not fail over: %v", seen)
	}

	setSlots(t, st, app, []int{a, b}, []string{"unhealthy", "unhealthy"})
	if seen := previewHits(t, s, app, 4); seen["A"]+seen["B"] != 4 {
		t.Errorf("all-unhealthy must still be tried: %v", seen)
	}
}

func postReplicas(t *testing.T, s *Server, slug, n string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"replicas": {n}, "csrf_token": {"csrf"}}
	req := httptest.NewRequest(http.MethodPost, "/apps/"+slug+"/replicas", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestReplicasHandler: out-of-range counts are rejected with a flash and
// nothing changes; a running deployed app queues a `scale` deployment with
// the current image; a stopped app just records the count.
func TestReplicasHandler(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Port: 8080, Image: "nginx:1"})
	cur, err := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "manual", Status: "running", ImageTag: "nginx:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAppCurrentDeployment(app.ID, cur.ID); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, rt: &fakeRuntime{}}

	for _, bad := range []string{"0", "6", "abc"} {
		rec := postReplicas(t, s, "web", bad)
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "flash=") {
			t.Errorf("replicas=%s: %d %q, want a flash redirect", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
	if a, _ := st.GetAppByID(app.ID); a.Replicas != 1 {
		t.Fatalf("rejected counts changed replicas to %d", a.Replicas)
	}

	rec := postReplicas(t, s, "web", "3")
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/deployments/") {
		t.Fatalf("scale redirect = %q, want the queued deployment", loc)
	}
	d, err := st.GetDeployment(strings.TrimPrefix(loc, "/deployments/"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "scale" || d.Status != "queued" || d.ImageTag != "nginx:1" {
		t.Errorf("scale row = kind %q status %q image %q", d.Kind, d.Status, d.ImageTag)
	}
	if a, _ := st.GetAppByID(app.ID); a.Replicas != 3 {
		t.Errorf("replicas = %d, want 3", a.Replicas)
	}

	if err := st.UpdateAppStatus(app.ID, "stopped"); err != nil {
		t.Fatal(err)
	}
	rec = postReplicas(t, s, "web", "2")
	if loc := rec.Header().Get("Location"); strings.HasPrefix(loc, "/deployments/") || !strings.Contains(loc, "flash=") {
		t.Errorf("stopped app must just record the count, got %q", loc)
	}
	if a, _ := st.GetAppByID(app.ID); a.Replicas != 2 {
		t.Errorf("replicas = %d, want 2", a.Replicas)
	}
}

// logsRuntime serves a fixed line per container on follow, then holds the
// stream open until the request ends (a real follow tail).
type logsRuntime struct{ fakeRuntime }

func (l *logsRuntime) Inspect(context.Context, string) (runtime.Info, error) {
	return runtime.Info{Running: true}, nil
}

func (l *logsRuntime) Logs(ctx context.Context, name string, follow bool, _ int) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		_, _ = stdcopy.NewStdWriter(pw, stdcopy.Stdout).Write([]byte("hello from " + name + "\n"))
		<-ctx.Done()
		pw.Close()
	}()
	return pr, nil
}

func getLogs(t *testing.T, s *Server, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Body.String()
}

// TestLogsMergeReplicas: every slot's stream merges into one SSE with [rN]
// prefixes; ?replica=r2 narrows to one slot.
func TestLogsMergeReplicas(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Port: 8080})
	setSlots(t, st, app, []int{1, 2}, []string{"", ""})
	s := &Server{store: st, rt: &logsRuntime{}}

	body := getLogs(t, s, "/apps/web/logs")
	for _, want := range []string{"data: [r1] hello from dm-web", "data: [r2] hello from dm-web-r2"} {
		if !strings.Contains(body, want) {
			t.Errorf("merged stream missing %q:\n%s", want, body)
		}
	}
	body = getLogs(t, s, "/apps/web/logs?replica=r2")
	if !strings.Contains(body, "[r2] hello from dm-web-r2") || strings.Contains(body, "hello from dm-web\n") {
		t.Errorf("?replica=r2 must narrow to slot 2:\n%s", body)
	}
}

// TestLogsFollowScaling: a panel opened on a 1-replica app picks up a
// second replica added while it is open (no reload) — the stream switches
// to [rN] prefixes and says so.
func TestLogsFollowScaling(t *testing.T) {
	old := logsResyncInterval
	logsResyncInterval = 40 * time.Millisecond
	t.Cleanup(func() { logsResyncInterval = old })

	st, app := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Port: 8080})
	setSlots(t, st, app, []int{1}, []string{""})
	s := &Server{store: st, rt: &logsRuntime{}}

	go func() {
		time.Sleep(120 * time.Millisecond)
		_ = st.UpsertAppReplica(store.AppReplica{AppID: app.ID, Slot: 2, ContainerName: "dm-web-r2", HostPort: 2})
	}()
	body := getLogs(t, s, "/apps/web/logs")
	for _, want := range []string{
		"data: hello from dm-web", // before scaling: unprefixed
		"replicas changed: now streaming r1, r2",
		"data: [r2] hello from dm-web-r2", // the new replica joined the stream
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream missing %q:\n%s", want, body)
		}
	}
}

// TestMetricsJSONReplicaFilter: the chart feed totals replicas by default,
// narrows with ?replica=r2, and rejects a malformed filter.
func TestMetricsJSONReplicaFilter(t *testing.T) {
	st, app := replicaTestEnv(t, store.App{Name: "Web", Slug: "web", Status: "running", Port: 8080})
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_ = st.InsertMetric(app.ID, store.Metric{Slot: 1, TS: ts, MemBytes: 10 << 20})
	_ = st.InsertMetric(app.ID, store.Metric{Slot: 2, TS: ts, MemBytes: 30 << 20})
	s := &Server{store: st}
	get := func(q string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/apps/web/metrics"+q, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "tok"})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	if code, body := get(""); code != 200 || !strings.Contains(body, `"v":40`) {
		t.Errorf("total = %d %s, want mem 40 MB", code, body)
	}
	if code, body := get("?replica=r2"); code != 200 || !strings.Contains(body, `"v":30`) || strings.Contains(body, `"v":40`) {
		t.Errorf("r2 = %d %s, want mem 30 MB only", code, body)
	}
	if code, _ := get("?replica=zz"); code != http.StatusBadRequest {
		t.Errorf("bad filter = %d, want 400", code)
	}
}
