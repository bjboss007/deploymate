package httpserver

import (
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/store"
)

var rootDirRe = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)

// cleanRootDirectory turns what the owner typed into a safe repository-relative
// folder, or says why it isn't one. "" and "." mean the repository root. It is the
// only gate before the value reaches filepath.Join in the build, so it refuses
// anything that could leave the checkout: absolute paths, "..", backslashes, odd
// characters.
func cleanRootDirectory(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." || raw == "/" {
		return "", ""
	}
	if len(raw) > 128 || strings.HasPrefix(raw, "/") || !rootDirRe.MatchString(raw) {
		return "", "Use a folder path inside the repository, like site or services/api (letters, digits, dot, dash, underscore and slash)."
	}
	// Judge what was typed, not what it cleans to: "a/../b" is harmless once cleaned,
	// but nobody typing ".." means to stay put, so refuse it rather than guess.
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", "The folder can't contain \"..\" — it must stay inside the repository."
		}
	}
	return path.Clean(raw), ""
}

// handleAppRootDirectory sets the folder (inside the repository) an app builds from —
// for monorepos, or a repo where the app lives in a subfolder, such as this project's
// website in site/.
func (s *Server) handleAppRootDirectory(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	dir, why := cleanRootDirectory(r.FormValue("root_directory"))
	if why != "" {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(why), http.StatusSeeOther)
		return
	}
	if err := s.store.UpdateAppRootDirectory(app.ID, dir); err != nil {
		slog.Error("apps: set root directory", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	shown := dir
	if shown == "" {
		shown = "the repository root"
	}
	_ = s.store.RecordEvent(app.ID, store.EventRootDirChanged, "build folder set to "+shown)
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Saved — the next deploy builds from "+shown+"."), http.StatusSeeOther)
}
