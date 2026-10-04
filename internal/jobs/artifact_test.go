package jobs

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/runtime"
	"github.com/habibmuhammad/deploymate/internal/store"
)

const ghToken = "github_pat_TEST"

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

// fakeGH serves GitHub's artifact endpoints for repo acme/web: run → its
// artifact list, and artifact id → zip bytes via a 302 to a storage host.
type fakeGH struct {
	mu        sync.Mutex
	calls     int
	runs      map[int64][]ghArtifact // run id → artifacts
	zips      map[int64][]byte       // artifact id → bytes
	blobAuth  string
	srv, blob *httptest.Server
}

type ghArtifact struct {
	ID      int64
	Name    string
	Expired bool
	Digest  string // "" = null
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	g := &fakeGH{runs: map[int64][]ghArtifact{}, zips: map[int64][]byte{}}
	g.blob = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.blobAuth = r.Header.Get("Authorization")
		g.mu.Unlock()
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/blob/"), 10, 64)
		w.Write(g.zips[id])
	}))
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.calls++
		g.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+ghToken {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"message":"Bad credentials"}`)
			return
		}
		p := r.URL.Path
		switch {
		case strings.HasPrefix(p, "/repos/acme/web/actions/runs/") && strings.HasSuffix(p, "/artifacts"):
			id, _ := strconv.ParseInt(strings.Split(p, "/")[6], 10, 64)
			arts, ok := g.runs[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"message":"Not Found"}`)
				return
			}
			var parts []string
			for _, a := range arts {
				digest := "null"
				if a.Digest != "" {
					digest = fmt.Sprintf("%q", a.Digest)
				}
				parts = append(parts, fmt.Sprintf(`{"id":%d,"name":%q,"size_in_bytes":%d,"expired":%v,"expires_at":"2026-10-03T11:32:54Z","digest":%s}`,
					a.ID, a.Name, len(g.zips[a.ID]), a.Expired, digest))
			}
			fmt.Fprintf(w, `{"total_count":%d,"artifacts":[%s]}`, len(arts), strings.Join(parts, ","))
		case strings.HasPrefix(p, "/repos/acme/web/actions/artifacts/") && strings.HasSuffix(p, "/zip"):
			http.Redirect(w, r, g.blob.URL+"/blob/"+strings.Split(p, "/")[6], http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(func() { g.srv.Close(); g.blob.Close() })
	return g
}

func sumOf(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }

// artifactSetup is a worker, a prebuilt-mode app linked to a source (with an
// encrypted token), a fake GitHub, and a build seam recording what it saw.
type artifactSetup struct {
	w    *Worker
	fake *fakeRT
	st   *store.Store
	app  store.App
	gh   *fakeGH
	gs   store.GitSource

	buildCalls  int
	buildDF     string // Dockerfile seen at build time
	buildJar    string // app.jar seen at build time
	buildTag    string
	buildErr    error
	buildCtxDir string
}

func newArtifactSetup(t *testing.T) *artifactSetup {
	t.Helper()
	w, fake, st := newTestWorker(t)
	app := seedApp(t, st)
	gh := newFakeGH(t)
	w.SetGitHubAPI(gh.srv.URL)
	enc, err := crypto.Encrypt(w.encKey, ghToken)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := st.CreateGitSource(store.GitSource{
		Provider: "github", RepoURL: "https://github.com/acme/web.git", CloneMethod: "deploy_key",
		DefaultBranch: "main", APITokenEnc: enc,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAppGitSource(app.ID, gs.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateAppDeployMode(app.ID, store.DeployModeArtifact, "", ""); err != nil {
		t.Fatal(err)
	}
	app, _ = st.GetAppByID(app.ID)
	fake.info = runtime.Info{Running: true} // a previous version is serving
	s := &artifactSetup{w: w, fake: fake, st: st, app: app, gh: gh, gs: gs}
	w.buildFn = func(_ context.Context, dir, _, tag string, _ func(string)) error {
		s.buildCalls++
		s.buildTag, s.buildCtxDir = tag, dir
		df, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
		jar, _ := os.ReadFile(filepath.Join(dir, "app.jar"))
		s.buildDF, s.buildJar = string(df), string(jar)
		return s.buildErr
	}
	return s
}

func (s *artifactSetup) queueRun(t *testing.T, runID int64, number int) store.Deployment {
	t.Helper()
	d, err := s.st.CreateDeployment(store.Deployment{
		AppID: s.app.ID, Kind: "deploy", Status: "queued", Trigger: "ci",
		CommitSHA: "abc123def4567890", CommitMessage: "ship it", CIRun: runID, CIRunNumber: number,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (s *artifactSetup) goodRun(t *testing.T, runID int64, jar string) {
	t.Helper()
	z := zipOf(t, map[string]string{"app.jar": jar})
	s.gh.zips[runID*10] = z
	s.gh.runs[runID] = []ghArtifact{{ID: runID * 10, Name: "deploymate-app", Digest: sumOf(z)}}
}

func (s *artifactSetup) result(t *testing.T, d store.Deployment) store.Deployment {
	t.Helper()
	s.w.process(context.Background(), d)
	got, err := s.st.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestArtifactDeploySuccess: download → extract the one JAR → wrap → build →
// the usual swap, with the image recorded and the working files cleaned up.
func TestArtifactDeploySuccess(t *testing.T) {
	s := newArtifactSetup(t)
	s.goodRun(t, 100, "REAL-JAR-BYTES")
	d := s.queueRun(t, 100, 4)

	got := s.result(t, d)
	if got.Status != "running" || got.Error != "" {
		t.Fatalf("deployment = %q (%s), want running", got.Status, got.Error)
	}
	if s.buildCalls != 1 || s.buildJar != "REAL-JAR-BYTES" {
		t.Fatalf("build calls=%d jar=%q, want one build of the artifact's jar", s.buildCalls, s.buildJar)
	}
	if !strings.HasPrefix(s.buildDF, "FROM eclipse-temurin:21-jre") {
		t.Errorf("Dockerfile = %q, want the default Java 21 wrapper", s.buildDF)
	}
	if s.buildTag != "deploymate/apps/web:"+d.ID || got.ImageTag != s.buildTag {
		t.Errorf("image tag = %q / %q", s.buildTag, got.ImageTag)
	}
	imgs, _ := s.st.ListImages(s.app.ID)
	if len(imgs) != 1 || imgs[0].Tag != s.buildTag {
		t.Errorf("images = %+v, want the wrapped image recorded (rollback + prune + /stats)", imgs)
	}
	specs := s.fake.createdSpecs()
	if len(specs) != 1 || specs[0].Image != s.buildTag || !strings.Contains(strings.Join(specs[0].Env, " "), "GIT_SHA=abc123def4567890") {
		t.Errorf("container specs = %+v", specs)
	}
	app, _ := s.st.GetAppByID(s.app.ID)
	if app.CurrentDeploymentID != d.ID || app.Status != "running" {
		t.Errorf("app = current %q status %q", app.CurrentDeploymentID, app.Status)
	}
	if _, err := os.Stat(filepath.Dir(s.buildCtxDir)); !os.IsNotExist(err) {
		t.Errorf("the working directory %s must be removed after the deploy", filepath.Dir(s.buildCtxDir))
	}
	if s.gh.blobAuth != "" {
		t.Errorf("the storage host saw Authorization %q — the token leaked across the redirect", s.gh.blobAuth)
	}
	lines, _ := s.st.ListBuildLogs(d.ID, 0)
	if log := strings.Join(lines, "\n"); !strings.Contains(log, "downloaded") || !strings.Contains(log, "wrapping app.jar") {
		t.Errorf("build log should narrate the steps:\n%s", log)
	}
}

func TestArtifactDeployUsesTheAppsJavaVersion(t *testing.T) {
	s := newArtifactSetup(t)
	if err := s.st.UpdateAppRuntime(s.app.ID, "java:17"); err != nil {
		t.Fatal(err)
	}
	s.goodRun(t, 100, "J")
	s.result(t, s.queueRun(t, 100, 1))
	if !strings.HasPrefix(s.buildDF, "FROM eclipse-temurin:17-jre") {
		t.Errorf("Dockerfile = %q, want Java 17 from the app's runtime", s.buildDF)
	}
}

// TestArtifactDeployFailures: every failure names its cause, never reaches
// the build or the swap, and leaves the running version alone.
func TestArtifactDeployFailures(t *testing.T) {
	good := zipOf(t, map[string]string{"app.jar": "J"})
	for _, tc := range []struct {
		name  string
		setup func(s *artifactSetup, t *testing.T) int64 // returns the run id to queue
		want  string
	}{
		{"no artifact of that name", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.zips[1] = good
			s.gh.runs[200] = []ghArtifact{{ID: 1, Name: "something-else"}}
			return 200
		}, `uploaded no artifact named "deploymate-app" (it has: something-else)`},
		{"run with no artifacts left (retention ended)", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.runs[210] = []ghArtifact{}
			return 210
		}, "has no artifacts any more"},
		{"expired artifact", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.zips[2] = good
			s.gh.runs[201] = []ghArtifact{{ID: 2, Name: "deploymate-app", Expired: true}}
			return 201
		}, "has expired"},
		{"digest mismatch", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.zips[3] = good
			s.gh.runs[202] = []ghArtifact{{ID: 3, Name: "deploymate-app", Digest: "sha256:" + strings.Repeat("0", 64)}}
			return 202
		}, "integrity check"},
		{"two jars", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.zips[4] = zipOf(t, map[string]string{"a.jar": "1", "b.jar": "2"})
			s.gh.runs[203] = []ghArtifact{{ID: 4, Name: "deploymate-app"}}
			return 203
		}, "exactly one .jar"},
		{"no jar", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.zips[5] = zipOf(t, map[string]string{"readme.txt": "x"})
			s.gh.runs[204] = []ghArtifact{{ID: 5, Name: "deploymate-app"}}
			return 204
		}, "no .jar found"},
		{"not a zip", func(s *artifactSetup, t *testing.T) int64 {
			s.gh.zips[6] = []byte("not a zip at all")
			s.gh.runs[205] = []ghArtifact{{ID: 6, Name: "deploymate-app"}}
			return 205
		}, "not a valid zip"},
		{"unknown run (404)", func(s *artifactSetup, t *testing.T) int64 { return 999 }, "could not find acme/web"},
		{"token rejected (401)", func(s *artifactSetup, t *testing.T) int64 {
			enc, _ := crypto.Encrypt(s.w.encKey, "revoked-token")
			if err := s.st.SetGitSourceAPIToken(s.gs.ID, enc); err != nil {
				t.Fatal(err)
			}
			return 100
		}, "token was rejected"},
		{"no token set", func(s *artifactSetup, t *testing.T) int64 {
			if err := s.st.SetGitSourceAPIToken(s.gs.ID, ""); err != nil {
				t.Fatal(err)
			}
			return 100
		}, "no GitHub token is set"},
		{"unparseable repo URL", func(s *artifactSetup, t *testing.T) int64 {
			if _, err := s.st.DB().Exec(`UPDATE git_sources SET repo_url = '/local/bare/repo.git' WHERE id = ?`, s.gs.ID); err != nil {
				t.Fatal(err)
			}
			return 100
		}, "cannot read owner/repo"},
		{"not a CI deployment", func(s *artifactSetup, t *testing.T) int64 { return 0 }, "deploys from GitHub Actions runs"},
		{"the wrapper build fails", func(s *artifactSetup, t *testing.T) int64 {
			s.buildErr = errors.New("build failed: pull access denied for eclipse-temurin")
			return 100
		}, "pull access denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newArtifactSetup(t)
			s.goodRun(t, 100, "J")
			runID := tc.setup(s, t)
			got := s.result(t, s.queueRun(t, runID, 3))
			if got.Status != "failed" || !strings.Contains(got.Error, tc.want) {
				t.Fatalf("deployment = %q %q, want failed containing %q", got.Status, got.Error, tc.want)
			}
			if len(s.fake.createdSpecs()) != 0 {
				t.Error("a failed deploy must never reach the container swap")
			}
			if tc.name != "the wrapper build fails" && s.buildCalls != 0 {
				t.Errorf("build ran %d times before the failure was caught", s.buildCalls)
			}
			// Zero-downtime: the previous version is still running, so the
			// app is NOT marked failed.
			if a, _ := s.st.GetAppByID(s.app.ID); a.Status == "failed" {
				t.Error("a failed prebuilt deploy took the app down")
			}
		})
	}
}

// TestArtifactRollbackNeedsNoGitHub: a rollback to a prebuilt image reuses
// the kept local image — it works with GitHub unreachable and the token
// dead.
func TestArtifactRollbackNeedsNoGitHub(t *testing.T) {
	s := newArtifactSetup(t)
	s.gh.srv.Close() // GitHub is gone
	old, err := s.st.CreateDeployment(store.Deployment{AppID: s.app.ID, Kind: "deploy", Status: "running", Trigger: "ci", ImageTag: "deploymate/apps/web:old", CIRun: 1, CIRunNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := s.st.CreateDeployment(store.Deployment{AppID: s.app.ID, Kind: "rollback", Status: "queued", Trigger: "rollback", ImageTag: old.ImageTag})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.result(t, rb); got.Status != "running" {
		t.Fatalf("rollback = %q (%s)", got.Status, got.Error)
	}
	if specs := s.fake.createdSpecs(); len(specs) != 1 || specs[0].Image != "deploymate/apps/web:old" {
		t.Errorf("rollback specs = %+v", specs)
	}
}

// TestBuildModeAppsKeepTheirOldPath: a build-mode app's deploy still goes to
// the clone-and-build path (it fails here only because the fixture has no
// git source) — prebuilt mode changes nothing for apps that did not opt in.
func TestBuildModeAppsKeepTheirOldPath(t *testing.T) {
	w, _, st := newTestWorker(t)
	app := seedApp(t, st)
	d, _ := st.CreateDeployment(store.Deployment{AppID: app.ID, Kind: "deploy", Status: "queued", Trigger: "webhook"})
	w.process(context.Background(), d)
	got, _ := st.GetDeployment(d.ID)
	if got.Status != "failed" || !strings.Contains(got.Error, "no git source connected") {
		t.Errorf("build-mode deploy = %q %q, want the git path's own error", got.Status, got.Error)
	}
}

// A Spring Boot fat jar gets the Spring logo: the framework is detected from
// the jar's entries, stored on the app, and an inconclusive jar never clears
// it.
func TestArtifactDeployDetectsSpringBoot(t *testing.T) {
	s := newArtifactSetup(t)
	fatJar := zipOf(t, map[string]string{
		"META-INF/MANIFEST.MF":                "Main-Class: org.springframework.boot.loader.JarLauncher",
		"BOOT-INF/lib/spring-boot-3.4.4.jar":  "x",
		"BOOT-INF/classes/com/acme/App.class": "x",
	})
	s.goodRun(t, 300, string(fatJar))
	if got := s.result(t, s.queueRun(t, 300, 30)); got.Status != "running" {
		t.Fatalf("deployment = %q (%s)", got.Status, got.Error)
	}
	app, _ := s.st.GetAppByID(s.app.ID)
	if app.Stack != "spring" {
		t.Errorf("app.Stack = %q, want spring", app.Stack)
	}

	// A later jar that is not recognisably Spring must not erase what is known.
	s.goodRun(t, 301, "PLAIN-JAR")
	if got := s.result(t, s.queueRun(t, 301, 31)); got.Status != "running" {
		t.Fatalf("deployment = %q (%s)", got.Status, got.Error)
	}
	if app, _ = s.st.GetAppByID(s.app.ID); app.Stack != "spring" {
		t.Errorf("an inconclusive scan cleared the stack: %q", app.Stack)
	}
}

// A prebuilt deploy provisions the services its artifact's deploymate.yml
// declares, exactly as a git build would, before the container is created.
func TestArtifactDeployAppliesManifest(t *testing.T) {
	s := newArtifactSetup(t)
	z := zipOf(t, map[string]string{
		"app.jar":        "J",
		"deploymate.yml": "services:\n  - redis\n",
	})
	s.gh.zips[1000] = z
	s.gh.runs[100] = []ghArtifact{{ID: 1000, Name: "deploymate-app", Digest: sumOf(z)}}
	d := s.queueRun(t, 100, 1)
	got := s.result(t, d)
	if got.Status != "running" {
		t.Fatalf("deployment = %q (%s)", got.Status, got.Error)
	}
	log := func() string {
		lines, _ := s.st.ListBuildLogs(d.ID, 0)
		return strings.Join(lines, "\n")
	}()
	for _, want := range []string{"manifest files in the artifact: deploymate.yml", "manifest (dev): redis"} {
		if !strings.Contains(log, want) {
			t.Errorf("build log lacks %q:\n%s", want, log)
		}
	}
	project, _ := s.st.GetProjectByID(s.app.ProjectID)
	svcs, _ := s.st.ListServices(project.ID)
	found := false
	for _, sv := range svcs {
		if sv.Type == "redis" {
			found = true
		}
	}
	if !found {
		t.Errorf("redis was not provisioned from the artifact's manifest; services = %+v", svcs)
	}
}

// A malformed manifest fails the deploy with its own message (as in a git build).
func TestArtifactDeployRejectsBadManifest(t *testing.T) {
	s := newArtifactSetup(t)
	z := zipOf(t, map[string]string{"app.jar": "J", "deploymate.yml": "services: [unknown-thing\n"})
	s.gh.zips[1000] = z
	s.gh.runs[100] = []ghArtifact{{ID: 1000, Name: "deploymate-app", Digest: sumOf(z)}}
	got := s.result(t, s.queueRun(t, 100, 1))
	if got.Status != "failed" || !strings.Contains(got.Error, "deploymate.yml") {
		t.Errorf("deployment = %q (%s), want a failed deploy naming deploymate.yml", got.Status, got.Error)
	}
	if s.buildCalls != 0 {
		t.Error("a bad manifest must stop the deploy before the build")
	}
}
