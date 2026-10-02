package httpserver

import (
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/store"
)

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s *Server) handleEnvVarCreate(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.FormValue("key"))
	value := r.FormValue("value")
	isSecret := r.FormValue("is_secret") == "on"
	if !envKeyRe.MatchString(key) || len(key) > 128 {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Keys must be letters, digits, and underscores, starting with a letter."), http.StatusSeeOther)
		return
	}
	valueEnc, err := crypto.Encrypt(s.encKey, value)
	if err != nil {
		slog.Error("env: encrypt", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_ = s.store.RecordEvent(app.ID, store.EventEnvChanged, "env var "+key+" set")
	if _, err := s.store.UpsertEnvVar(store.EnvVar{
		AppID: app.ID, Key: key, ValueEnc: valueEnc, IsSecret: isSecret,
	}); err != nil {
		slog.Error("env: upsert", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Environment variable saved. Redeploy to apply."), http.StatusSeeOther)
}

// envRowKeyRe matches the per-row field names of the variables form
// (key_0, value_0, secret_0, key_1, …) — the "Add variable" button appends
// rows with the next index.
var envRowKeyRe = regexp.MustCompile(`^key_(\d+)$`)

// envItem is one variable to save: from a row of the form or a pasted line.
type envItem struct {
	Key, Value string
	Secret     bool
}

// parseEnvRows reads the form's rows in index order. A row with no key and
// no value is skipped (the empty row the form starts with); errors name the
// 1-based position among the submitted rows.
func parseEnvRows(form map[string][]string) (items []envItem, errs []string) {
	var idx []int
	for name := range form {
		if m := envRowKeyRe.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			idx = append(idx, n)
		}
	}
	sort.Ints(idx)
	get := func(name string, i int) string {
		if v := form[fmt.Sprintf("%s_%d", name, i)]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	pos := 0
	for _, i := range idx {
		key, val := strings.TrimSpace(get("key", i)), get("value", i)
		if key == "" && val == "" {
			continue
		}
		pos++
		if !envKeyRe.MatchString(key) || len(key) > 128 {
			if key == "" {
				errs = append(errs, fmt.Sprintf("row %d: the name is empty", pos))
			} else {
				errs = append(errs, fmt.Sprintf("row %d: %q is not a valid name (letters, digits, underscores; must start with a letter or underscore)", pos, truncate(key, 40)))
			}
			continue
		}
		if len(val) > maxEnvValueLen {
			errs = append(errs, fmt.Sprintf("row %d: the value of %s is too long", pos, key))
			continue
		}
		items = append(items, envItem{Key: key, Value: val, Secret: get("secret", i) == "on"})
	}
	return items, errs
}

// handleEnvVarBulk saves every variable of the form in one request: the rows
// added with "Add variable" plus, optionally, a pasted .env block. All or
// nothing: any invalid row/line rejects the whole submission (nothing is
// written), so the owner fixes it and saves again instead of ending up
// half-configured. Existing keys are updated in place; sensitive-looking
// names (PASSWORD, SECRET, TOKEN, …) are always stored masked.
func (s *Server) handleEnvVarBulk(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	back := func(msg string) {
		http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL(msg), http.StatusSeeOther)
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	items, errs := parseEnvRows(r.PostForm)
	pasted, perrs := parseEnvLines(r.PostFormValue("bulk"))
	errs = append(errs, perrs...)
	if len(errs) > 0 {
		if len(errs) > 3 {
			errs = append(errs[:3], fmt.Sprintf("and %d more", len(errs)-3))
		}
		back("Nothing saved — " + strings.Join(errs, "; ") + ".")
		return
	}
	pasteSecret := r.PostFormValue("is_secret") == "on"
	for _, v := range pasted {
		items = append(items, envItem{Key: v.Key, Value: v.Value, Secret: pasteSecret})
	}
	if len(items) == 0 {
		back("Add at least one variable (a name and a value) before saving.")
		return
	}
	if len(items) > maxBulkEnvLines {
		back(fmt.Sprintf("Nothing saved — more than %d variables in one save.", maxBulkEnvLines))
		return
	}

	existing, err := s.store.ListEnvVars(app.ID)
	if err != nil {
		slog.Error("env: list", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	had := map[string]bool{}
	for _, e := range existing {
		had[e.Key] = true
	}
	// A key repeated in one save keeps its last value (and one write).
	last := map[string]envItem{}
	order := []string{}
	for _, it := range items {
		if _, dup := last[it.Key]; !dup {
			order = append(order, it.Key)
		}
		last[it.Key] = it
	}
	added, updated := 0, 0
	for _, key := range order {
		it := last[key]
		valueEnc, err := crypto.Encrypt(s.encKey, it.Value)
		if err != nil {
			slog.Error("env: encrypt", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if _, err := s.store.UpsertEnvVar(store.EnvVar{
			AppID: app.ID, Key: key, ValueEnc: valueEnc, IsSecret: it.Secret || looksSecret(key),
		}); err != nil {
			slog.Error("env: upsert", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if had[key] {
			updated++
		} else {
			added++
		}
	}
	// Names only — never values — go in the event log.
	_ = s.store.RecordEvent(app.ID, store.EventEnvChanged, "env vars set: "+truncate(strings.Join(order, ", "), 300))
	back(fmt.Sprintf("Saved %d variable(s) (%d new, %d updated). Redeploy to apply.", len(order), added, updated))
}

func (s *Server) handleEnvVarDelete(w http.ResponseWriter, r *http.Request) {
	app, ok := s.appFromRequest(w, r)
	if !ok {
		return
	}
	_ = s.store.RecordEvent(app.ID, store.EventEnvRemoved, "env var removed")
	if err := s.store.DeleteEnvVar(chi.URLParam(r, "id")); err != nil {
		slog.Error("env: delete", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/"+app.Slug+"?flash="+flashURL("Environment variable removed. Redeploy to apply."), http.StatusSeeOther)
}
