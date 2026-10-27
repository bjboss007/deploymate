package site

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

// Every page of the website loads in a phone-wide frame; none may be wider than the phone, and every docs table cell
// must have received its card label from site.js. Same method as internal/httpserver/responsive_browser_test.go.
const auditPage = `<!doctype html><meta charset=utf-8><body><script>
(async () => {
  const pages = %s, bad = [];
  for (const w of [375, 320]) for (const p of pages) {
    const f = document.createElement("iframe");
    f.style.cssText = "width:" + w + "px;height:800px;border:0;position:absolute;left:0;top:0";
    f.src = p;
    document.body.appendChild(f);
    await new Promise((r) => f.addEventListener("load", r));
    await new Promise((r) => setTimeout(r, 400));
    const d = f.contentDocument;
    const wide = d.documentElement.scrollWidth;
    if (wide > w) {
      const culprit = [...d.body.querySelectorAll("*")].filter((e) => e.getBoundingClientRect().right > w + 1 || e.scrollWidth > w + 1)
        .filter((e) => !e.closest(".nav, .tour-tabs, pre, .install-line")).filter((e, _, all) => !all.some((o) => o !== e && e.contains(o))).slice(0, 3)
        .map((e) => e.tagName.toLowerCase() + "[" + e.textContent.trim().slice(0, 20) + "|" + Math.round(e.getBoundingClientRect().right) + "]" + (e.className ? "." + String(e.className).split(" ")[0] : "")).join(", ");
      bad.push(p + " @" + w + ": page is " + wide + "px wide; reaching past the edge: " + culprit);
    }
    d.querySelectorAll(".doc table").forEach((t) => {
      if (!t.querySelector("thead th, tr.head-row")) return; // key/value tables have no column labels
      const n = [...t.querySelectorAll("tbody td")].filter((td) => td.colSpan === 1 && !td.hasAttribute("data-label")).length;
      if (n) bad.push(p + " @" + w + ": " + n + " table cells have no label");
    });
    f.remove();
  }
  await fetch("/__result", {method: "POST", body: JSON.stringify(bad)});
})();
</script>`

func TestWebsiteFitsAPhone(t *testing.T) {
	chrome := chromeBinary()
	if chrome == "" {
		t.Skip("no Chrome found (set DEPLOYMATE_CHROME)")
	}
	var pages []string
	for _, glob := range []string{"*.html", "docs/*.html"} {
		m, _ := filepath.Glob(glob)
		for _, f := range m {
			pages = append(pages, "/"+filepath.ToSlash(f))
		}
	}
	if len(pages) < 10 {
		t.Fatalf("only %d pages found", len(pages))
	}
	list, _ := json.Marshal(pages)
	done := make(chan []byte, 1)
	files := http.FileServer(http.Dir("."))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/__result":
			b, _ := io.ReadAll(r.Body)
			done <- b
		case "/__audit":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(strings.Replace(auditPage, "%s", string(list), 1)))
		default:
			w.Header().Set("Cache-Control", "no-store")
			files.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cmd := exec.Command(chrome, "--headless", "--disable-gpu", "--no-sandbox", "--user-data-dir="+t.TempDir(), srv.URL+"/__audit")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	select {
	case b := <-done:
		var bad []string
		if err := json.Unmarshal(b, &bad); err != nil {
			t.Fatalf("audit result: %.300s", b)
		}
		for _, m := range bad {
			t.Error(m)
		}
	case <-time.After(90 * time.Second):
		t.Fatal("the browser never reported back")
	}
}
