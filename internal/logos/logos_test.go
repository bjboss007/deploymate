package logos

import (
	"regexp"
	"strings"
	"testing"
)

func TestEveryLogoIsUsable(t *testing.T) {
	colour := regexp.MustCompile(`^(#[0-9A-Fa-f]{6})?$`)
	for _, l := range Choices() {
		if l.Key == "" || l.Name == "" || !strings.HasPrefix(l.Path, "M") || len(l.Path) < 15 {
			t.Errorf("logo %q is malformed: name=%q pathLen=%d", l.Key, l.Name, len(l.Path))
		}
		if !colour.MatchString(l.Color) {
			t.Errorf("logo %q has a bad colour %q", l.Key, l.Color)
		}
		if strings.ContainsAny(l.Path, "<>\"") {
			t.Errorf("logo %q path contains markup characters", l.Key)
		}
	}
	for _, k := range []string{"react", "java", "spring", "postgres", "redis", "mysql", "node", "python", "docker"} {
		if !Has(k) {
			t.Errorf("missing logo %q", k)
		}
	}
	if Get("nope").Key != "docker" {
		t.Error("unknown keys must fall back to the container mark")
	}
}
