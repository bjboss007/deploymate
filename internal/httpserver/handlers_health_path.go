package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// maxHealthPathLen bounds the health path: it becomes a docker label value
// and a probe URL, and nothing legitimate is anywhere near this long.
const maxHealthPathLen = 200

// healthPathProbeTimeout bounds each replica's check-on-save probe so a
// hung app cannot stall the form submit.
const healthPathProbeTimeout = 3 * time.Second

// normalizeHealthPath validates the health path form field. Empty resets to
// the default "/". A path must be origin-relative (start with "/", no
// scheme or host — "//host" would parse as one), carry no whitespace or
// control characters, and have no fragment; a query string is allowed.
func normalizeHealthPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return store.DefaultHealthPath, nil
	}
	if len(p) > maxHealthPathLen {
		return "", fmt.Errorf("health path is longer than %d characters", maxHealthPathLen)
	}
	if !strings.HasPrefix(p, "/") {
		return "", errors.New("health path must start with / (e.g. /healthz)")
	}
	for _, r := range p {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", errors.New("health path cannot contain spaces or control characters")
		}
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || strings.HasPrefix(p, "//") {
		return "", errors.New("health path must be a path on the app, not a URL (e.g. /healthz)")
	}
	if strings.Contains(p, "#") {
		return "", errors.New("health path cannot contain a # fragment")
	}
	return p, nil
}

// handleAppHealthPath saves the app's health path, then probes every
// running replica at it so the operator learns immediately whether the
// path works. The monitor uses the new path from its next probe; the
// Traefik healthcheck label changes on the next deploy. Traefik ejects a
// replica whose health path is not 2xx/3xx (the monitor only needs < 500),
// so a wrong path could eject every replica at once — the flash says so.
func (s *Server) handleAppHealthPath(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := "/apps/" + app.Slug
	path, err := normalizeHealthPath(r.FormValue("health_path"))
	if err != nil {
		http.Redirect(w, r, back+"?flash="+flashURL(strings.ToUpper(err.Error()[:1])+err.Error()[1:]+"."), http.StatusSeeOther)
		return
	}
	prev := app.HealthPath
	if err := s.store.UpdateAppHealthPath(app.ID, path); err != nil {
		slog.Error("apps: set health path", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if path != prev {
		_ = s.store.RecordEvent(app.ID, store.EventHealthPathChanged, fmt.Sprintf("health path %s → %s", prev, path))
	}
	app.HealthPath = path

	if app.Status != "running" || app.Port <= 0 {
		http.Redirect(w, r, back+"?flash="+flashURL("Health path saved as "+path+" — it is checked once the app runs; Traefik picks it up on the next deploy."), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?flash="+flashURL(s.healthPathVerdict(r.Context(), app)), http.StatusSeeOther)
}

// healthPathVerdict probes each replica at the app's health path and turns
// the results into the flash message: all good, or which replicas would be
// ejected by Traefik and why.
func (s *Server) healthPathVerdict(ctx context.Context, app store.App) string {
	client := &http.Client{
		Timeout: healthPathProbeTimeout,
		// Report redirects as-is: Traefik's healthcheck does not follow
		// them either, and a 3xx already counts as healthy.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var ok, bad []string
	for _, sl := range s.appSlots(app) {
		if sl.HostPort <= 0 {
			continue
		}
		name := "r" + strconv.Itoa(sl.Slot)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(sl.HostPort)+app.HealthPath, nil)
		if err != nil {
			bad = append(bad, name+" (bad request)")
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			bad = append(bad, name+" (no answer)")
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			ok = append(ok, fmt.Sprintf("%s %d", name, resp.StatusCode))
		} else {
			bad = append(bad, fmt.Sprintf("%s %d", name, resp.StatusCode))
		}
	}
	switch {
	case len(ok) == 0 && len(bad) == 0:
		return "Health path saved as " + app.HealthPath + "."
	case len(bad) == 0:
		return "Health path saved as " + app.HealthPath + " — " + strings.Join(ok, ", ") +
			". The monitor uses it now; Traefik's healthcheck picks it up on the next deploy."
	default:
		return "Health path saved as " + app.HealthPath + ", but it failed on " + strings.Join(bad, ", ") +
			" — Traefik removes replicas that don't answer 2xx/3xx there, so fix the path (or the app) before deploying behind Traefik."
	}
}
