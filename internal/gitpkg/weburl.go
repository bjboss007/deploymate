package gitpkg

import "strings"

// WebURL normalizes a repo URL (SSH or HTTPS, any forge) to its browsable
// web form: "git@github.com:owner/repo.git" and
// "https://github.com/owner/repo.git" both become
// "https://github.com/owner/repo".
func WebURL(repoURL string) string {
	url := strings.TrimSuffix(repoURL, ".git")

	switch {
	case strings.HasPrefix(url, "git@"):
		// git@github.com:owner/repo → https://github.com/owner/repo
		host, path, ok := strings.Cut(url, ":")
		if ok {
			return "https://" + strings.TrimPrefix(host, "git@") + "/" + path
		}
		return ""
	case strings.HasPrefix(url, "ssh://"):
		// ssh://git@github.com/owner/repo → https://github.com/owner/repo
		rest := strings.TrimPrefix(url, "ssh://")
		if _, path, ok := strings.Cut(rest, "/"); ok {
			return "https://" + strings.TrimPrefix(strings.SplitN(rest, "/", 2)[0], "git@") + "/" + path
		}
		return ""
	case strings.HasPrefix(url, "http://"), strings.HasPrefix(url, "https://"):
		return url
	}
	return url // local paths and unknown schemes pass through
}

// CommitURL builds the browsable link for a commit on any forge that uses
// the /commit/<sha> convention (GitHub, GitLab, Gitea, Bitbucket all do).
// Empty when there is no commit or no browsable repo.
func CommitURL(repoURL, sha string) string {
	web := WebURL(repoURL)
	if web == "" || sha == "" || !strings.HasPrefix(web, "http") {
		return ""
	}
	return web + "/commit/" + sha
}
