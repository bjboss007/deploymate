package httpserver

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"DeployMate":          "deploymate",
		"My Cool Project":     "my-cool-project",
		"  spaced  out  ":     "spaced-out",
		"hack//the//planet":   "hack-the-planet",
		"Ünïcödé":             "n-c-d",
		"already-a-slug":      "already-a-slug",
		"!!!":                 "",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
