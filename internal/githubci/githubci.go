// Package githubci is the small slice of the GitHub REST API prebuilt
// deploys need (docs/specs/prebuilt-deploys.md): read a repo, list a
// workflow's successful runs, list a run's artifacts, and download an
// artifact. Every behavior here was verified against real GitHub (spike S1,
// 2026-10-02): Bearer auth; artifact download is a 302 to a signed blob URL
// that must be fetched WITHOUT the Authorization header; the artifact
// `digest` is "sha256:<hex>" of the zip archive.
package githubci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is GitHub's REST API. DEPLOYMATE_GITHUB_API_URL overrides
// it for tests only (the e2e's fake GitHub).
const DefaultBaseURL = "https://api.github.com"

// maxJSONBytes bounds any API JSON response we decode.
const maxJSONBytes = 8 << 20

// ErrTooLarge reports an artifact bigger than the caller's cap.
var ErrTooLarge = errors.New("artifact exceeds the size limit")

// APIError is a non-2xx GitHub response. Status carries the meaning the
// callers map to messages: 401 bad/expired/missing token, 403 missing
// permission (or org approval pending), 404 no such thing OR no access.
type APIError struct {
	Op      string // what we were doing, e.g. "list run artifacts"
	Status  int
	Message string // GitHub's own message, when it sent one
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("github: %s: HTTP %d: %s", e.Op, e.Status, e.Message)
	}
	return fmt.Sprintf("github: %s: HTTP %d", e.Op, e.Status)
}

// Client talks to the GitHub REST API with one token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client // API calls; nil = a 30s-timeout client
}

// New builds a client; an empty baseURL means DefaultBaseURL.
func New(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Token: token}
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// ValidRepo reports whether s is a well-formed "owner/name" — checked before
// it is ever placed in a URL path.
func ValidRepo(s string) bool { return repoRe.MatchString(s) }

// ParseRepoURL extracts "owner/name" from the repo URL forms a git source
// can hold: https://host/o/r(.git), git@host:o/r(.git), ssh://git@host/o/r(.git).
func ParseRepoURL(u string) (string, bool) {
	u = strings.TrimSpace(u)
	var p string
	switch {
	case strings.HasPrefix(u, "git@"):
		i := strings.Index(u, ":")
		if i < 0 {
			return "", false
		}
		p = u[i+1:]
	case strings.Contains(u, "://"):
		parsed, err := url.Parse(u)
		if err != nil {
			return "", false
		}
		p = strings.TrimPrefix(parsed.Path, "/")
	default:
		return "", false
	}
	p = strings.TrimSuffix(strings.TrimSuffix(p, "/"), ".git")
	if !ValidRepo(p) {
		return "", false
	}
	return p, true
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) newRequest(ctx context.Context, op, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("github: %s: %w", op, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "deploymate")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

// apiError builds an APIError from a failed response, reading GitHub's
// "message" field when the body has one.
func apiError(op string, resp *http.Response) *APIError {
	e := &APIError{Op: op, Status: resp.StatusCode}
	var body struct {
		Message string `json:"message"`
	}
	if b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10)); err == nil {
		if json.Unmarshal(b, &body) == nil {
			e.Message = body.Message
		}
	}
	return e
}

// getJSON GETs path (relative to BaseURL) and decodes the JSON into out.
func (c *Client) getJSON(ctx context.Context, op, p string, out any) error {
	req, err := c.newRequest(ctx, op, c.BaseURL+p)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("github: %s: %w", op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return apiError(op, resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out); err != nil {
		return fmt.Errorf("github: %s: decode response: %w", op, err)
	}
	return nil
}

func checkRepo(repo string) error {
	if !ValidRepo(repo) {
		return fmt.Errorf("github: invalid repository %q", repo)
	}
	return nil
}

// Repo is the part of a repository's metadata we use.
type Repo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// GetRepo reads a repository — the cheapest "does this token reach this
// repo at all" probe (needs only the implicit Metadata: read).
func (c *Client) GetRepo(ctx context.Context, repo string) (Repo, error) {
	var r Repo
	if err := checkRepo(repo); err != nil {
		return r, err
	}
	err := c.getJSON(ctx, "read repository", "/repos/"+repo, &r)
	return r, err
}

// OtherPrivateRepos counts private repositories OTHER than repo that the
// token can see (first 100 only; more reports there may be further ones).
// Fine-grained tokens can always read public repos, so only private ones
// say anything about how widely the token was scoped (spike S1-B: a token
// created with "All repositories" saw every private repo of the owner).
func (c *Client) OtherPrivateRepos(ctx context.Context, repo string) (n int, more bool, err error) {
	if err := checkRepo(repo); err != nil {
		return 0, false, err
	}
	var list []Repo
	if err := c.getJSON(ctx, "list accessible repositories",
		"/user/repos?per_page=100&affiliation=owner,organization_member", &list); err != nil {
		return 0, false, err
	}
	for _, r := range list {
		if r.Private && !strings.EqualFold(r.FullName, repo) {
			n++
		}
	}
	return n, len(list) >= 100, nil
}

// Run is one workflow run (a row of the list-runs response).
type Run struct {
	ID          int64  `json:"id"`
	RunNumber   int    `json:"run_number"`
	RunAttempt  int    `json:"run_attempt"`
	Event       string `json:"event"`
	Conclusion  string `json:"conclusion"`
	HeadBranch  string `json:"head_branch"`
	HeadSHA     string `json:"head_sha"`
	Path        string `json:"path"`
	DisplayName string `json:"display_title"`
	HeadCommit  struct {
		Message string `json:"message"`
	} `json:"head_commit"`
	HeadRepository struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

// ListSuccessfulRuns returns the newest successful runs of a workflow on a
// branch, restricted to the events a deploy may come from (push and
// workflow_dispatch) and to the repository's own code (never a fork's).
// workflowFile is the workflow's path or file name; GitHub accepts the file
// name.
func (c *Client) ListSuccessfulRuns(ctx context.Context, repo, workflowFile, branch string, limit int) ([]Run, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	q := url.Values{"branch": {branch}, "status": {"success"}, "per_page": {fmt.Sprint(limit)}}
	var resp struct {
		Runs []Run `json:"workflow_runs"`
	}
	p := "/repos/" + repo + "/actions/workflows/" + url.PathEscape(path.Base(workflowFile)) + "/runs?" + q.Encode()
	if err := c.getJSON(ctx, "list workflow runs", p, &resp); err != nil {
		return nil, err
	}
	var out []Run
	for _, r := range resp.Runs {
		if r.Event != "push" && r.Event != "workflow_dispatch" {
			continue
		}
		if r.HeadRepository.FullName != "" && !strings.EqualFold(r.HeadRepository.FullName, repo) {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// Artifact is one uploaded artifact of a run.
type Artifact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_in_bytes"`
	Expired   bool      `json:"expired"`
	ExpiresAt time.Time `json:"expires_at"`
	// Digest is "sha256:<hex>" of the zip archive; empty when GitHub has none.
	Digest string `json:"digest"`
}

// ListRunArtifacts lists a run's artifacts (first 100).
func (c *Client) ListRunArtifacts(ctx context.Context, repo string, runID int64) ([]Artifact, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
	var resp struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	p := fmt.Sprintf("/repos/%s/actions/runs/%d/artifacts?per_page=100", repo, runID)
	if err := c.getJSON(ctx, "list run artifacts", p, &resp); err != nil {
		return nil, err
	}
	return resp.Artifacts, nil
}

// DownloadArtifact streams an artifact's zip into dst, returning the number
// of bytes and the sha256 (hex) of what was written. GitHub answers the API
// call with a 302 to a signed blob URL (valid ~1 minute, possibly on a
// different host); that URL is fetched with a clean client that carries NO
// Authorization header — the token must never reach the blob host. More
// than maxBytes returns ErrTooLarge.
func (c *Client) DownloadArtifact(ctx context.Context, repo string, artifactID int64, dst io.Writer, maxBytes int64) (int64, string, error) {
	const op = "download artifact"
	if err := checkRepo(repo); err != nil {
		return 0, "", err
	}
	req, err := c.newRequest(ctx, op, fmt.Sprintf("%s/repos/%s/actions/artifacts/%d/zip", c.BaseURL, repo, artifactID))
	if err != nil {
		return 0, "", err
	}
	noRedirect := *c.httpClient()
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("github: %s: %w", op, err)
	}
	defer resp.Body.Close()

	body := resp.Body
	switch {
	case resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently ||
		resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusSeeOther:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return 0, "", fmt.Errorf("github: %s: redirect without a Location", op)
		}
		// A clean request: no token, no GitHub headers. The redirect URL is
		// never logged or put in an error (it carries a signed token).
		blobReq, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
		if err != nil {
			return 0, "", fmt.Errorf("github: %s: bad redirect target", op)
		}
		blobReq.Header.Set("User-Agent", "deploymate")
		blobResp, err := (&http.Client{}).Do(blobReq) // ctx bounds it; the transfer can be large
		if err != nil {
			return 0, "", fmt.Errorf("github: %s: fetch from storage: %s", op, redactURLErr(err))
		}
		defer blobResp.Body.Close()
		if blobResp.StatusCode/100 != 2 {
			return 0, "", &APIError{Op: op + " (storage)", Status: blobResp.StatusCode}
		}
		body = blobResp.Body
	case resp.StatusCode/100 == 2:
		// Some servers (and the test fake) may answer directly.
	default:
		return 0, "", apiError(op, resp)
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(body, maxBytes+1))
	if err != nil {
		return n, "", fmt.Errorf("github: %s: %w", op, err)
	}
	if n > maxBytes {
		return n, "", ErrTooLarge
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// redactURLErr strips the URL from a *url.Error — the redirect URL is signed.
func redactURLErr(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
