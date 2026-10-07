package httpserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCleanRootDirectory(t *testing.T) {
	for raw, want := range map[string]string{
		"": "", ".": "", "/": "", "  site ": "site", "site/": "site", "services/api": "services/api",
		"a/./b": "a/b", "my-app_1.2/sub": "my-app_1.2/sub",
	} {
		if got, why := cleanRootDirectory(raw); got != want || why != "" {
			t.Errorf("clean(%q) = %q, %q; want %q", raw, got, why, want)
		}
	}
	for _, bad := range []string{"..", "../x", "a/../b", "a/..", "/etc", "/abs/path", `a\b`, "a b", "a;b", "~", "-rf", strings.Repeat("a", 129), "a//b/../.."} {
		if got, why := cleanRootDirectory(bad); why == "" {
			t.Errorf("clean(%q) = %q accepted; it can leave the repository or is malformed", bad, got)
		}
	}
}

func TestSetBuildFolder(t *testing.T) {
	e := newPrebuiltEnv(t)
	_, loc := e.post(t, "/apps/api/root-directory", url.Values{"root_directory": {"site"}})
	if f := flashOf(t, loc); !strings.Contains(f, "Saved") || !strings.Contains(f, "site") {
		t.Errorf("flash = %q", f)
	}
	if app, _ := e.reload(t); app.RootDirectory != "site" {
		t.Errorf("root directory = %q", app.RootDirectory)
	}
	// An escape attempt is refused and changes nothing.
	_, loc = e.post(t, "/apps/api/root-directory", url.Values{"root_directory": {"../../etc"}})
	if f := flashOf(t, loc); strings.Contains(f, "Saved") {
		t.Errorf("an escaping path was accepted: %q", f)
	}
	if app, _ := e.reload(t); app.RootDirectory != "site" {
		t.Errorf("a refused save changed the folder to %q", app.RootDirectory)
	}
	// Empty goes back to the repository root, and both are recorded.
	e.post(t, "/apps/api/root-directory", url.Values{"root_directory": {""}})
	if app, _ := e.reload(t); app.RootDirectory != "" {
		t.Errorf("root directory = %q, want the root", app.RootDirectory)
	}
	evs, _ := e.st.ListEvents(e.app.ID, 10)
	n := 0
	for _, ev := range evs {
		if ev.Kind == "root_directory_changed" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d folder-change events, want 2", n)
	}
}

func TestAPIConfigRootDirectory(t *testing.T) {
	e := newPrebuiltEnv(t)
	tok := e.createToken(t, "agent", "provision", "90")
	if code, res, _ := e.send(t, http.MethodPatch, "/api/v1/apps/api/config", tok, map[string]any{"root_directory": "site"}); code != http.StatusOK {
		t.Fatalf("set folder = %d %v", code, res)
	}
	if app, _ := e.reload(t); app.RootDirectory != "site" {
		t.Errorf("root directory = %q", app.RootDirectory)
	}
	if code, _, _ := e.send(t, http.MethodPatch, "/api/v1/apps/api/config", tok, map[string]any{"root_directory": "../up"}); code != http.StatusConflict {
		t.Errorf("an escaping folder via the API = %d, want 409", code)
	}
}
