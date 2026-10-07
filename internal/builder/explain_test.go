package builder

import (
	"strings"
	"testing"
)

func TestExplain(t *testing.T) {
	for _, tc := range []struct {
		err      string
		wantNil  bool
		wantHint string // substring of the title
	}{
		{`swap: staged container failed readiness probe: Get "http://127.0.0.1:1/": EOF — the container exited with code 1`, false, "crashed while starting"},
		{`swap: staged container failed readiness probe: EOF — the container was killed for lack of memory`, false, "ran out of memory while starting"},
		{`swap: staged container failed readiness probe: Get "http://127.0.0.1:1/": EOF`, false, "never answered"},
		{`manifest: service did not become ready within 60s — check its logs`, false, "database or cache"},
		{`build ran out of memory — the build was killed for lack of memory`, false, "build ran out of memory"},
		{`the GitHub token was rejected for acme/web (HTTP 401: Bad credentials)`, false, "rejected the access token"},
		{`the run uploaded no artifact named "deploymate-app"`, false, "CI build output"},
		{`git clone: Permission denied (publickey).`, false, "couldn't fetch the code"},
		{`manifest: unknown service type "mongo"`, false, "deploymate.yml"},
		{`something nobody has seen before`, true, ""},
		{``, true, ""},
	} {
		got := Explain(tc.err)
		if tc.wantNil {
			if got != nil {
				t.Errorf("Explain(%q) = %+v, want nil", tc.err, got)
			}
			continue
		}
		if got == nil || !strings.Contains(got.Title, tc.wantHint) || len(got.Hints) == 0 {
			t.Errorf("Explain(%q) = %+v, want a title containing %q and hints", tc.err, got, tc.wantHint)
		}
	}
}

// Hints may only link to tabs that exist on the app page.
func TestExplainTabsExist(t *testing.T) {
	valid := map[string]bool{"": true, "logs": true, "variables": true, "settings": true, "deployments": true, "overview": true}
	for _, e := range []string{"readiness probe killed for lack of memory", "readiness probe exited with code 2", "readiness probe", "service did not become ready", "ran out of memory", "token was rejected", "exactly one .jar", "permission denied (publickey)", "manifest: x"} {
		x := Explain(e)
		if x == nil {
			t.Fatalf("no explanation for %q", e)
		}
		for _, h := range x.Hints {
			if !valid[h.Tab] || h.Text == "" {
				t.Errorf("%q: bad hint %+v", e, h)
			}
		}
	}
}

func TestExplainMissingBuildx(t *testing.T) {
	for _, in := range []string{
		"unknown flag: --progress\nUsage:  docker [OPTIONS] COMMAND [ARG...]",
		"docker: 'buildx' is not a docker command.",
	} {
		ex := Explain(in)
		if ex == nil || !strings.Contains(ex.Title, "buildx") {
			t.Errorf("Explain(%q) = %+v, want the missing-buildx explanation", in, ex)
		}
	}
}
