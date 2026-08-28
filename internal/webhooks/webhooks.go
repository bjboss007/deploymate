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
}

// ParseGitHubPush extracts the push data from a GitHub push payload.
func ParseGitHubPush(body []byte) (Push, error) {
	var p struct {
		Ref  string `json:"ref"`
		After string `json:"after"`
		Head struct {
			Message string `json:"message"`
		} `json:"head_commit"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Push{}, err
	}
	return Push{Ref: p.Ref, CommitSHA: p.After, CommitMessage: p.Head.Message}, nil
}

// ParseGitLabPush extracts the push data from a GitLab push payload.
func ParseGitLabPush(body []byte) (Push, error) {
	var p struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Commits []struct {
			Message string `json:"message"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return Push{}, err
	}
	msg := ""
	if len(p.Commits) > 0 {
		msg = p.Commits[len(p.Commits)-1].Message
	}
	return Push{Ref: p.Ref, CommitSHA: p.After, CommitMessage: msg}, nil
}

// BranchFromRef converts refs/heads/main to main.
func BranchFromRef(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// DeliveryCache dedupes webhook deliveries for 24h so provider retries and
// double sends never deploy twice.
type DeliveryCache struct {
	mu    sync.Mutex
	items map[string]time.Time
}

// NewDeliveryCache builds an empty cache.
func NewDeliveryCache() *DeliveryCache {
	return &DeliveryCache{items: make(map[string]time.Time)}
}

// Seen records a delivery ID; true means it was already processed.
func (c *DeliveryCache) Seen(provider, deliveryID string) bool {
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
	key := provider + ":" + deliveryID
	if _, ok := c.items[key]; ok {
		return true
	}
	c.items[key] = now
	return false
}
