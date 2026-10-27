package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/demo"
	"github.com/habibmuhammad/deploymate/internal/services"
	"github.com/habibmuhammad/deploymate/internal/sse"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// chromeBinary finds a Chrome to drive; the test is skipped without one (set DEPLOYMATE_CHROME to point at it).
func chromeBinary() string {
	if p := os.Getenv("DEPLOYMATE_CHROME"); p != "" {
		return p
	}
	for _, p := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if path, err := exec.LookPath(p); err == nil {
			return path
		}
	}
	return ""
}

// stripFraming lets the pages load inside the audit page's frames; the real headers are covered by their own test.
type stripFraming struct{ http.ResponseWriter }

func (w stripFraming) WriteHeader(code int) {
	w.Header().Del("Content-Security-Policy")
	w.Header().Del("X-Frame-Options")
	w.ResponseWriter.WriteHeader(code)
}
func (w stripFraming) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// The audit page loads every dashboard page in a frame the width of a phone and reports, per page and width, whether
// anything pushes the page wider than the phone and whether a table cell reached the card layout without its label.
// The frame's own width is the viewport, so the media queries behave as on a real phone; `overflow-x: clip` on <html> is
// switched off first because it would hide exactly the failure being looked for.
const auditPage = `<!doctype html><meta charset=utf-8><body><script>
(async () => {
  const pages = %s, widths = [375, 320], bad = [];
  for (const w of widths) for (const p of pages) {
    const f = document.createElement("iframe");
    f.style.cssText = "width:" + w + "px;height:800px;border:0;position:absolute;left:0;top:0";
    f.src = p;
    document.body.appendChild(f);
    await new Promise((r) => f.addEventListener("load", r));
    await new Promise((r) => setTimeout(r, 400));
    const d = f.contentDocument;
    const s = d.createElement("style");
    s.textContent = "html{overflow-x:visible!important}";
    d.head.appendChild(s);
    const wide = d.documentElement.scrollWidth;
    if (wide > w) {
      const culprit = [...d.body.querySelectorAll("*")].filter((e) => e.getBoundingClientRect().right > w + 1 || e.scrollWidth > w + 1).filter((e, _, all) => !all.some((o) => o !== e && e.contains(o))).slice(0, 3)
        .map((e) => e.tagName.toLowerCase() + (e.className ? "." + String(e.className).split(" ")[0] : "")).join(", ");
      bad.push(p + " @" + w + ": page is " + wide + "px wide; reaching past the edge: " + culprit);
    }
    d.querySelectorAll("table.deploy-table").forEach((t) => {
      if (!t.querySelector("thead th")) return;
      const unlabelled = [...t.querySelectorAll("tbody td")].filter((td) => td.colSpan === 1 && !td.hasAttribute("data-label"));
      if (unlabelled.length) bad.push(p + " @" + w + ": " + unlabelled.length + " table cells have no label");
    });
    f.remove();
  }
  await fetch("/__result", {method: "POST", body: JSON.stringify(bad)});
})();
</script>`

func TestDashboardFitsAPhoneAndTablesBecomeLabelledCards(t *testing.T) {
	chrome := chromeBinary()
	if chrome == "" {
		t.Skip("no Chrome found (set DEPLOYMATE_CHROME)")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "dm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := [32]byte{9}
	hash, _ := auth.HashPassword("demo-demo-demo")
	if err := demo.Seed(st, key, time.Now(), hash); err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{}
	s := New(st, rt, services.NewProvisioner(st, rt, key, "test-net"), sse.NewBroker(), key, "off", "", t.TempDir(), nil, nil)
	s.SetDemoHost(demo.Host{})
	inner := s.Handler()
	var srv *httptest.Server
	done := make(chan []byte, 1)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__result" {
			b, _ := io.ReadAll(r.Body)
			done <- b
			return
		}
		if r.URL.Path != "/__audit" {
			inner.ServeHTTP(stripFraming{w}, r)
			return
		}
		// Sign in on the visitor's behalf, then hand the session cookie to the browser with the audit page.
		jar, _ := cookiejar.New(nil)
		c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := c.PostForm(srv.URL+"/login", url.Values{"email": {"demo@deploymate.dev"}, "password": {"demo-demo-demo"}})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		resp.Body.Close()
		u, _ := url.Parse(srv.URL)
		for _, ck := range jar.Cookies(u) {
			http.SetCookie(w, ck)
		}
		pages, _ := json.Marshal([]string{"/projects", "/projects/storefront", "/apps/api", "/apps/invoicer", "/services/storefront-postgres",
			"/server", "/nodes", "/security", "/signins", "/stats", "/alerts", "/settings/tokens", "/settings/github"})
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(strings.Replace(auditPage, "%s", string(pages), 1)))
	}))
	t.Cleanup(srv.Close)

	cmd := exec.Command(chrome, "--headless", "--disable-gpu", "--no-sandbox", "--user-data-dir="+t.TempDir(), srv.URL+"/__audit")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var bad []string
	select {
	case b := <-done:
		if err := json.Unmarshal(b, &bad); err != nil {
			t.Fatalf("audit result: %.300s", b)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the browser never reported back")
	}
	for _, b := range bad {
		t.Error(b)
	}
}
