package templates

import "testing"

func TestNavCurrentPicksTheSidebarItem(t *testing.T) {
	for path, want := range map[string]string{
		"/":                        "/projects",
		"/projects/acme":           "/projects",
		"/apps/api":                "/projects",
		"/services/redis":          "/projects",
		"/server":                  "/server",
		"/server/history":          "/server",
		"/security":                "/security",
		"/signins":                 "/signins",
		"/nodes":                   "/nodes",
		"/stats":                   "/stats",
		"/settings/github":         "/settings/github",
		"/settings/github/connect": "/settings/github",
		"/settings/tokens":         "/settings/tokens",
		"/alerts":                  "/alerts",
		"/serverless":              "/projects", // a prefix is not a match
	} {
		if got := navCurrent(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}
