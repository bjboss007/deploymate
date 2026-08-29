package gitpkg

import "testing"

func TestWebURL(t *testing.T) {
	cases := map[string]string{
		"git@github.com:bjboss007/VGG-final-Project.git": "https://github.com/bjboss007/VGG-final-Project",
		"ssh://git@github.com/owner/repo.git":            "https://github.com/owner/repo",
		"https://github.com/owner/repo.git":              "https://github.com/owner/repo",
		"https://gitlab.com/group/proj":                  "https://gitlab.com/group/proj",
		"git@gitea.example.com:team/app.git":             "https://gitea.example.com/team/app",
		"/tmp/dm-fixture.git":                            "/tmp/dm-fixture",
	}
	for in, want := range cases {
		if got := WebURL(in); got != want {
			t.Errorf("WebURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCommitURL(t *testing.T) {
	got := CommitURL("git@github.com:owner/repo.git", "bf9bddd1234")
	want := "https://github.com/owner/repo/commit/bf9bddd1234"
	if got != want {
		t.Errorf("CommitURL = %q, want %q", got, want)
	}
	if got := CommitURL("git@github.com:owner/repo.git", ""); got != "" {
		t.Errorf("empty sha should yield empty URL, got %q", got)
	}
	if got := CommitURL("/tmp/dm-fixture.git", "abc123"); got != "" {
		t.Errorf("local paths should yield empty URL, got %q", got)
	}
}
