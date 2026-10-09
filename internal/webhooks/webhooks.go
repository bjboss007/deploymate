// Package webhooks verifies and parses incoming Git provider push events.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrUnverified is returned when a signature or token does not match.
var ErrUnverified = errors.New("webhooks: unverified request")

// VerifyGitHub checks the X-Hub-Signature-256 header against the raw body.
func VerifyGitHub(secret, signature string, body []byte) bool {
	prefix := "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}

// VerifyGitLab checks the X-GitLab-Token header (constant time).
func VerifyGitLab(secret, token string) bool {
	return subtle.ConstantTimeCompare([]byte(secret), []byte(token)) == 1
}

// Push is the minimal push-event data DeployMate deploys from.
type Push struct {
	Ref           string // refs/heads/main
	CommitSHA     string
	CommitMessage string
	// Files are the paths the push added, modified or removed. FilesKnown is
	// false when the payload cannot say (no commit list, or the provider's
	// 20-commit cap cut it off); callers must then assume everything changed.
	Files      []string
	FilesKnown bool
	// RepoFullName ("owner/name") and Deleted come from a GitHub push payload:
	// a pushed-then-deleted branch has nothing to deploy.
	RepoFullName string
	Deleted      bool
}

// maxPayloadCommits is how many commits GitHub and GitLab include in a push
// payload. A push with that many (or more) may have been truncated.
const maxPayloadCommits = 20

type commitFiles struct {
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

func collectFiles(commits []commitFiles) (files []string, known bool) {
	if len(commits) == 0 || len(commits) >= maxPayloadCommits {
		return nil, false
	}
	for _, c := range commits {
		files = append(files, c.Added...)
		files = append(files, c.Modified...)
		files = append(files, c.Removed...)
	}
	return files, true
}

// TouchesFolder reports whether a push could change what is built from root
// (a repository subfolder; "" is the whole repository). When the payload did
// not say which files changed it answers true: an unknown push deploys.
func (p Push) TouchesFolder(root string) bool {
	root = strings.Trim(strings.TrimPrefix(root, "./"), "/")
	if root == "" || !p.FilesKnown {
		return true
	}
	for _, f := range p.Files {
		if f == root || strings.HasPrefix(f, root+"/") {
			return true
		}
	}
	return false
}

// ParseGitHubPush extracts the push data from a GitHub push payload.
func ParseGitHubPush(body []byte) (Push, error) {
	var p struct {
		Ref   string `json:"ref"`
		After string `json:"after"`
		Head  struct {
			Message string `json:"message"`
		} `json:"head_commit"`
		Commits    []commitFiles `json:"commits"`
		Deleted    bool          `json:"deleted"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Push{}, err
	}
	files, known := collectFiles(p.Commits)
	return Push{Ref: p.Ref, CommitSHA: p.After, CommitMessage: p.Head.Message, Files: files, FilesKnown: known,
		RepoFullName: p.Repository.FullName, Deleted: p.Deleted || isZeroSHA(p.After)}, nil
}

// WorkflowRun is the data prebuilt deploys read from a GitHub `workflow_run`
// event (field names verified against real deliveries, spike S1).
type WorkflowRun struct {
	Action      string // requested | in_progress | completed
	RunID       int64
	RunNumber   int
	RunAttempt  int
	Name        string
	Path        string // e.g. .github/workflows/deploymate.yml
	Event       string // what triggered the run: push, pull_request, …
	Status      string
	Conclusion  string // success | failure | cancelled | …
	HeadBranch  string
	HeadSHA     string
	HeadMessage string
	HeadRepo    string // workflow_run.head_repository.full_name (the code's repo)
	Repo        string // repository.full_name (where the workflow lives)
}

// ParseGitHubWorkflowRun extracts a workflow_run payload.
func ParseGitHubWorkflowRun(body []byte) (WorkflowRun, error) {
	var p struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Run *struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Path       string `json:"path"`
			Event      string `json:"event"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadBranch string `json:"head_branch"`
			HeadSHA    string `json:"head_sha"`
			RunNumber  int    `json:"run_number"`
			RunAttempt int    `json:"run_attempt"`
			HeadCommit struct {
				Message string `json:"message"`
			} `json:"head_commit"`
			HeadRepository struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
		} `json:"workflow_run"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return WorkflowRun{}, err
	}
	if p.Run == nil || p.Run.ID == 0 {
		return WorkflowRun{}, errors.New("webhooks: not a workflow_run payload")
	}
	r := p.Run
	return WorkflowRun{
		Action: p.Action, RunID: r.ID, RunNumber: r.RunNumber, RunAttempt: r.RunAttempt,
		Name: r.Name, Path: r.Path, Event: r.Event, Status: r.Status, Conclusion: r.Conclusion,
		HeadBranch: r.HeadBranch, HeadSHA: r.HeadSHA, HeadMessage: r.HeadCommit.Message,
		HeadRepo: r.HeadRepository.FullName, Repo: p.Repository.FullName,
	}, nil
}

func isZeroSHA(sha string) bool {
	return sha != "" && strings.Trim(sha, "0") == ""
}

// ParseGitLabPush extracts the push data from a GitLab push payload.
func ParseGitLabPush(body []byte) (Push, error) {
	var p struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Commits []struct {
			Message string `json:"message"`
			commitFiles
		} `json:"commits"`
		Total int `json:"total_commits_count"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Push{}, err
	}
	msg := ""
	if len(p.Commits) > 0 {
		msg = p.Commits[len(p.Commits)-1].Message
	}
	cf := make([]commitFiles, len(p.Commits))
	for i, c := range p.Commits {
		cf[i] = c.commitFiles
	}
	files, known := collectFiles(cf)
	if p.Total > len(p.Commits) {
		known = false
	}
	return Push{Ref: p.Ref, CommitSHA: p.After, CommitMessage: msg, Files: files, FilesKnown: known}, nil
}

// BranchFromRef converts refs/heads/main to main.
func BranchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// DeliveryCache dedupes webhook deliveries for 24h so provider retries and
// double sends never deploy twice. Deliveries are keyed per git SOURCE:
// GitHub sends every webhook configured on a repo the same
// X-GitHub-Delivery GUID for one event, and each env's app (dev/stage/prod
// of one repo) has its own source and hook — a key without the source id
// would let the first hook to arrive swallow the rest.
type DeliveryCache struct {
	mu    sync.Mutex
	items map[string]time.Time
}

// NewDeliveryCache builds an empty cache.
func NewDeliveryCache() *DeliveryCache {
	return &DeliveryCache{items: make(map[string]time.Time)}
}

// Seen records a delivery for a source; true means that source already
// processed this delivery ID.
func (c *DeliveryCache) Seen(provider, sourceID, deliveryID string) bool {
	if deliveryID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, t := range c.items {
		if now.Sub(t) > 24*time.Hour {
			delete(c.items, k)
		}
	}
	key := provider + ":" + sourceID + ":" + deliveryID
	if _, ok := c.items[key]; ok {
		return true
	}
	c.items[key] = now
	return false
}
