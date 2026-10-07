package builder

import "strings"

// Hint is one thing to try. Tab names a tab of the app page ("logs",
// "variables", "settings") the hint's link opens; "" = no link.
type Hint struct {
	Text string
	Tab  string
}

// Explanation turns a failed deployment's error into plain words: what that
// failure usually means and what to do about it. It is advice, never a
// verdict — the raw error stays on the page next to it.
type Explanation struct {
	Title string
	Hints []Hint
}

// Explain recognises the failures DeployMate has seen in practice and
// returns nil for anything else (the raw error is then shown on its own).
func Explain(errText string) *Explanation {
	e := strings.ToLower(errText)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(e, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("unknown flag: --progress"), has("buildx") && has("not a docker command"), has("unknown shorthand flag") && has("buildx"):
		return &Explanation{
			Title: "This server's Docker has no build plugin (buildx)",
			Hints: []Hint{
				{"Install it on the server: sudo apt install docker-buildx-plugin (Docker's own repository) or sudo apt install docker-buildx (Ubuntu's package), then deploy again.", ""},
				{"Check it worked with: docker buildx version", ""},
				{"Re-running the DeployMate installer script also installs it.", ""},
			},
		}
	case has("readiness probe") && has("killed for lack of memory"):
		return &Explanation{
			Title: "The app ran out of memory while starting",
			Hints: []Hint{
				{"Give Docker more memory (Docker Desktop → Settings → Resources), or stop other containers.", ""},
				{"Lower the app's own memory use, for example a smaller JVM heap via JAVA_OPTS.", "variables"},
			},
		}
	case has("readiness probe") && has("exited with code"):
		return &Explanation{
			Title: "The app crashed while starting",
			Hints: []Hint{
				{"Read the container output in the log below — the crash is usually in its last lines.", ""},
				{"Check every setting the app needs is set: database URL and password, secrets, hosts.", "variables"},
			},
		}
	case has("readiness probe"):
		return &Explanation{
			Title: "The app started but never answered",
			Hints: []Hint{
				{"Make sure it listens on the port in the PORT environment variable — DeployMate sets it.", ""},
				{"Check the health path answers 2xx or 3xx on every replica.", "settings"},
				{"Open the app's logs to see whether it is stuck starting or has crashed.", "logs"},
			},
		}
	case has("service did not become ready"), has("manifest: service"):
		return &Explanation{
			Title: "A database or cache this app needs didn't start",
			Hints: []Hint{
				{"Open that service from the project page and read its log.", ""},
				{"A service pinned to an older version than its stored data (for example redis:7 on data written by Redis 8) cannot start — match the version, or use a fresh volume.", ""},
			},
		}
	case has("ran out of memory", "lack of memory"):
		return &Explanation{
			Title: "The build ran out of memory",
			Hints: []Hint{
				{"Give Docker more memory, or stop other containers while building.", ""},
				{"For JVM apps, build in CI instead: switch the app to Prebuilt (GitHub Actions).", "settings"},
			},
		}
	case has("token was rejected", "refused the token", "bad credentials"):
		return &Explanation{
			Title: "GitHub rejected the access token",
			Hints: []Hint{
				{"Create a new fine-grained token limited to this repository with Actions: read-only, and save it.", "settings"},
			},
		}
	case has("could not find the artifact", "has no artifacts any more", "uploaded no artifact named", "exactly one .jar", "no .jar found", "has expired", "not a valid zip", "integrity check"):
		return &Explanation{
			Title: "The CI build output isn't what DeployMate expects",
			Hints: []Hint{
				{"The workflow must upload exactly one .jar as an artifact with the configured name. Copy the generated workflow.", "settings"},
				{"An expired artifact only needs the workflow re-run on GitHub.", ""},
			},
		}
	case has("permission denied (publickey)", "could not read from remote", "authentication failed", "repository not found", "could not reach the repository"):
		return &Explanation{
			Title: "DeployMate couldn't fetch the code",
			Hints: []Hint{
				{"Check the deploy key is added to the repository with read access, and the repository URL and branch are right.", "settings"},
			},
		}
	case has("manifest:"):
		return &Explanation{
			Title: "deploymate.yml has a problem",
			Hints: []Hint{
				{"Fix the file in the repository — the error above names the line or entry.", ""},
			},
		}
	}
	return nil
}
