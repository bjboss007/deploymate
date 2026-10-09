// Package githubapp is DeployMate's side of "Connect GitHub": registering a
// GitHub App from a manifest, proving who DeployMate is to GitHub (an app JWT),
// and listing where the app is installed. Nothing here needs a personal token.
package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultAPI is GitHub's REST base URL.
const DefaultAPI = "https://api.github.com"

// DefaultWeb is github.com, where the manifest form is posted.
const DefaultWeb = "https://github.com"

// ManifestOpts describes the app DeployMate asks GitHub to create.
type ManifestOpts struct {
	Name        string // shown on GitHub; must be unique there (the owner can change it on the form)
	HomepageURL string
	RedirectURL string // where GitHub sends the browser with the one-time code
	SetupURL    string // where GitHub sends the browser after an installation
	WebhookURL  string // "" = no public address yet: the webhook is created inactive
}

// Manifest builds the JSON GitHub's "create app from manifest" form expects.
// Permissions are the least that cover cloning, reading CI runs and artifacts,
// and starting a workflow: contents (read), metadata (read), actions (write).
// installation and installation_repositories events need no subscription: GitHub
// sends them to every app.
func Manifest(o ManifestOpts) ([]byte, error) {
	if o.HomepageURL == "" || o.RedirectURL == "" {
		return nil, errors.New("manifest needs a homepage and a redirect URL")
	}
	hook := map[string]any{"url": o.WebhookURL, "active": o.WebhookURL != ""}
	if o.WebhookURL == "" {
		// GitHub insists on a URL even for an inactive hook.
		hook["url"] = "https://example.invalid/deploymate-not-configured"
	}
	m := map[string]any{
		"name":            clipName(o.Name),
		"url":             o.HomepageURL,
		"redirect_url":    o.RedirectURL,
		"public":          false,
		"description":     "Lets a self-hosted DeployMate clone your repositories and deploy on push.",
		"hook_attributes": hook,
		"default_permissions": map[string]string{
			"contents": "read",
			"metadata": "read",
			"actions":  "write",
		},
		"default_events": []string{"push", "workflow_run"},
	}
	if o.SetupURL != "" {
		m["setup_url"] = o.SetupURL
	}
	return json.Marshal(m)
}

// clipName keeps within GitHub's 34-character limit for app names.
func clipName(n string) string {
	n = strings.TrimSpace(n)
	if n == "" {
		n = "DeployMate"
	}
	if r := []rune(n); len(r) > 34 {
		n = string(r[:34])
	}
	return n
}

// NewFormURL is where the manifest is POSTed. org == "" is the signed-in
// person's own account; otherwise that organisation's.
func NewFormURL(web, org, state string) string {
	if web == "" {
		web = DefaultWeb
	}
	path := "/settings/apps/new"
	if org != "" {
		path = "/organizations/" + url.PathEscape(org) + "/settings/apps/new"
	}
	return web + path + "?state=" + url.QueryEscape(state)
}

// NewState is an unguessable value tying the redirect back to this browser.
func NewState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Credentials are what GitHub returns for a freshly created app.
type Credentials struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

// Client talks to the GitHub REST API (base URL overridable for tests).
type Client struct {
	API  string
	HTTP *http.Client
}

// New builds a Client; api "" means github.com.
func New(api string) *Client {
	if api == "" {
		api = DefaultAPI
	}
	return &Client{API: strings.TrimRight(api, "/"), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Message: apiMessage(body)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// APIError is a non-2xx answer from GitHub.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("GitHub answered %d: %s", e.Status, e.Message) }

func apiMessage(b []byte) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &m) == nil && m.Message != "" {
		return m.Message
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ConvertManifest exchanges the one-time code (valid for an hour) for the app's
// credentials. GitHub needs no authentication for this call; the code is the
// credential, so it must never be logged or shown.
func (c *Client) ConvertManifest(ctx context.Context, code string) (Credentials, error) {
	if code == "" || strings.ContainsAny(code, "/?#") {
		return Credentials{}, errors.New("invalid manifest code")
	}
	var cr Credentials
	if err := c.do(ctx, http.MethodPost, "/app-manifests/"+url.PathEscape(code)+"/conversions", "", &cr); err != nil {
		return Credentials{}, err
	}
	if cr.ID == 0 || cr.PEM == "" {
		return Credentials{}, errors.New("GitHub's answer had no app id or private key")
	}
	return cr, nil
}

// Installation is one place the app is installed.
type Installation struct {
	ID                  int64  `json:"id"`
	RepositorySelection string `json:"repository_selection"` // all | selected
	Account             struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"account"`
	SuspendedAt string `json:"suspended_at"`
}

// ListInstallations asks GitHub where this app is installed. It authenticates
// as the app itself with a short-lived JWT.
func (c *Client) ListInstallations(ctx context.Context, appID int64, pemKey string) ([]Installation, error) {
	jwt, err := AppJWT(appID, pemKey, time.Now())
	if err != nil {
		return nil, err
	}
	var out []Installation
	if err := c.do(ctx, http.MethodGet, "/app/installations?per_page=100", jwt, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AppJWT signs the token an app uses to prove who it is (RS256, valid ten
// minutes; issued a minute in the past to forgive clock drift).
func AppJWT(appID int64, pemKey string, now time.Time) (string, error) {
	key, err := ParseKey(pemKey)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// ParseKey reads the RSA private key GitHub issues (PKCS#1; PKCS#8 also accepted).
func ParseKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("the app's private key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("the app's private key could not be read")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("the app's private key is not RSA")
	}
	return rk, nil
}

// VerifyAppJWT checks a JWT's RS256 signature against a key and returns its
// issuer. Used by the test GitHub; kept here so signing and checking stay together.
func VerifyAppJWT(token string, pub *rsa.PublicKey, now time.Time) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, errors.New("not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return 0, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		return 0, errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, err
	}
	var c struct {
		Iss int64 `json:"iss"`
		Exp int64 `json:"exp"`
		Iat int64 `json:"iat"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return 0, err
	}
	if now.Unix() > c.Exp {
		return 0, errors.New("expired")
	}
	if c.Exp-c.Iat > 10*60+60 {
		return 0, errors.New("GitHub allows ten minutes at most")
	}
	return c.Iss, nil
}
