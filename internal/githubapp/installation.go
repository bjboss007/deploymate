package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scope narrows an installation token. A token without a scope can do everything the
// app can on every repository of the installation; every use in DeployMate asks for
// the least it needs: one repository (by GitHub's numeric id, which survives a rename)
// and only the permissions of the job.
type Scope struct {
	RepoID int64 // 0 = see RepoName
	// RepoName ("name", no owner) limits the token by name for a source whose id is not
	// known yet; ignored when RepoID is set. Both empty = every repository of the installation.
	RepoName    string
	Permissions map[string]string // e.g. {"contents": "read"}; nil = all the app's permissions
}

// key identifies a scope in the token cache.
func (sc Scope) key() string {
	perms := make([]string, 0, len(sc.Permissions))
	for k, v := range sc.Permissions {
		perms = append(perms, k+"="+v)
	}
	sort.Strings(perms)
	return fmt.Sprintf("%d|%s|%s", sc.RepoID, sc.RepoName, strings.Join(perms, ","))
}

// installationToken asks GitHub for a short-lived token for one installation
// (valid an hour), limited to the scope. The app authenticates with a JWT.
func (c *Client) installationToken(ctx context.Context, appID int64, pemKey string, installationID int64, sc Scope) (string, time.Time, error) {
	jwt, err := AppJWT(appID, pemKey, time.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	var in map[string]any
	if sc.RepoID != 0 || sc.RepoName != "" || len(sc.Permissions) > 0 {
		in = map[string]any{}
		switch {
		case sc.RepoID != 0:
			in["repository_ids"] = []int64{sc.RepoID}
		case sc.RepoName != "":
			in["repositories"] = []string{sc.RepoName}
		}
		if len(sc.Permissions) > 0 {
			in["permissions"] = sc.Permissions
		}
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installationID), jwt, in, &out); err != nil {
		return "", time.Time{}, err
	}
	if out.Token == "" {
		return "", time.Time{}, errors.New("GitHub's answer had no token")
	}
	if out.ExpiresAt.IsZero() {
		out.ExpiresAt = time.Now().Add(55 * time.Minute)
	}
	return out.Token, out.ExpiresAt, nil
}

// Tokens hands out installation tokens, reusing one until it is close to expiry
// so a burst of clones makes one request to GitHub. Tokens of different scopes
// are different tokens.
type Tokens struct {
	Client *Client
	mu     sync.Mutex
	cache  map[string]cachedToken
	now    func() time.Time // tests set it
}

type cachedToken struct {
	appID   int64
	token   string
	expires time.Time
}

// refreshBefore is how long before expiry a token is replaced.
const refreshBefore = 5 * time.Minute

// NewTokens builds a token source over a client.
func NewTokens(c *Client) *Tokens {
	return &Tokens{Client: c, cache: map[string]cachedToken{}, now: time.Now}
}

func cacheKey(installationID int64, sc Scope) string {
	return fmt.Sprintf("%d|%s", installationID, sc.key())
}

// Get returns a usable token for the installation with the given scope.
func (t *Tokens) Get(ctx context.Context, appID int64, pemKey string, installationID int64, sc Scope) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := cacheKey(installationID, sc)
	if c, ok := t.cache[k]; ok && c.appID == appID && t.now().Add(refreshBefore).Before(c.expires) {
		return c.token, nil
	}
	tok, exp, err := t.Client.installationToken(ctx, appID, pemKey, installationID, sc)
	if err != nil {
		return "", err
	}
	t.cache[k] = cachedToken{appID: appID, token: tok, expires: exp}
	return tok, nil
}

// Forget drops every cached token of an installation (after GitHub refused one).
func (t *Tokens) Forget(installationID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prefix := fmt.Sprintf("%d|", installationID)
	for k := range t.cache {
		if strings.HasPrefix(k, prefix) {
			delete(t.cache, k)
		}
	}
}

// Repo is a repository an installation can see.
type Repo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	CloneURL      string `json:"clone_url"`
	Archived      bool   `json:"archived"`
}

// nameRE is what GitHub allows in an owner or repository name.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// maxRepoPages bounds the listing (100 per page), so a huge organisation cannot
// make one page load unbounded.
const maxRepoPages = 10

// ListInstallationRepos lists the repositories the installation token can see.
func (c *Client) ListInstallationRepos(ctx context.Context, token string) ([]Repo, error) {
	var all []Repo
	for page := 1; page <= maxRepoPages; page++ {
		var out struct {
			Repositories []Repo `json:"repositories"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), token, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Repositories...)
		if len(out.Repositories) < 100 {
			break
		}
	}
	return all, nil
}

// BranchExists reports whether the repository ("owner/name") has the branch.
func (c *Client) BranchExists(ctx context.Context, token, fullName, branch string) (bool, error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok || !nameRE.MatchString(owner) || !nameRE.MatchString(name) || owner == "." || owner == ".." || name == "." || name == ".." {
		return false, errors.New("repository must be owner/name")
	}
	for _, seg := range strings.Split(branch, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false, errors.New("not a valid branch name")
		}
	}
	err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/branches/"+escapePath(branch), token, nil)
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return false, nil
	}
	return err == nil, err
}

// escapePath escapes each segment of a branch name but keeps its slashes.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
