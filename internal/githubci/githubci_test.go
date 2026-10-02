package githubci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "github_pat_TESTTOKEN"

// fakeGitHub is a minimal API server mirroring the response shapes captured
// from real GitHub (spike S1): Bearer auth (401 otherwise), a 302 to a
// separate "storage" host for downloads, JSON error bodies with "message".
type fakeGitHub struct {
	api, blob *httptest.Server
	zip       []byte
	blobAuth  atomic.Value // the Authorization header the blob host saw ("" = none)
}

func newFakeGitHub(t *testing.T, zip []byte) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{zip: zip}
	f.blobAuth.Store("")
	f.blob = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.blobAuth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "storage rejects Authorization", http.StatusBadRequest)
			return
		}
		w.Write(f.zip)
	}))
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			bad := "Bad credentials"
			if r.Header.Get("Authorization") == "" {
				bad = "Requires authentication"
			}
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"message":%q}`, bad)
			return
		}
		switch r.URL.Path {
		case "/repos/acme/app":
			fmt.Fprint(w, `{"full_name":"acme/app","private":true}`)
		case "/user/repos":
			fmt.Fprint(w, `[{"full_name":"acme/app","private":true},{"full_name":"acme/other","private":true},{"full_name":"acme/pub","private":false},{"full_name":"me/secret","private":true}]`)
		case "/repos/acme/app/actions/workflows/deploymate.yml/runs":
			if r.URL.Query().Get("branch") != "main" || r.URL.Query().Get("status") != "success" {
				t.Errorf("runs query = %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"total_count":4,"workflow_runs":[
			 {"id":4,"run_number":4,"run_attempt":1,"event":"push","conclusion":"success","head_branch":"main","head_sha":"d4","path":".github/workflows/deploymate.yml","head_commit":{"message":"newest"},"head_repository":{"full_name":"acme/app"}},
			 {"id":3,"run_number":3,"run_attempt":1,"event":"pull_request","conclusion":"success","head_branch":"main","head_sha":"d3","head_repository":{"full_name":"acme/app"}},
			 {"id":2,"run_number":2,"run_attempt":1,"event":"push","conclusion":"success","head_branch":"main","head_sha":"d2","head_repository":{"full_name":"evil/app"}},
			 {"id":1,"run_number":1,"run_attempt":2,"event":"workflow_dispatch","conclusion":"success","head_branch":"main","head_sha":"d1","head_repository":{"full_name":"acme/app"}}]}`)
		case "/repos/acme/app/actions/runs/4/artifacts":
			fmt.Fprint(w, `{"total_count":2,"artifacts":[
			 {"id":11,"name":"other","size_in_bytes":10,"expired":false,"expires_at":"2026-10-03T11:32:54Z","digest":null},
			 {"id":12,"name":"deploymate-app","size_in_bytes":1583,"expired":false,"expires_at":"2026-10-03T11:32:54Z","digest":"sha256:abc"}]}`)
		case "/repos/acme/app/actions/artifacts/12/zip":
			http.Redirect(w, r, f.blob.URL+"/blob/12?sig=SECRETSIGNATURE", http.StatusFound)
		case "/repos/acme/app/actions/artifacts/13/zip": // answered directly, no redirect
			w.Write(f.zip)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
		}
	}))
	t.Cleanup(func() { f.api.Close(); f.blob.Close() })
	return f
}

func TestParseRepoURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/app.git":           "acme/app",
		"https://github.com/acme/app":               "acme/app",
		"https://github.com/acme/app/":              "acme/app",
		"git@github.com:acme/app.git":               "acme/app",
		"ssh://git@github.com/acme/app.git":         "acme/app",
		"https://github.com/Acme-Org/my.repo_1.git": "Acme-Org/my.repo_1",
	} {
		if got, ok := ParseRepoURL(in); !ok || got != want {
			t.Errorf("ParseRepoURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "acme/app", "https://github.com/acme", "https://github.com/a/b/c", "git@github.com:acme", "https://github.com/ac me/app", "/local/bare/repo.git"} {
		if got, ok := ParseRepoURL(bad); ok {
			t.Errorf("ParseRepoURL(%q) = %q, want failure", bad, got)
		}
	}
}

func TestInvalidRepoNeverReachesAURL(t *testing.T) {
	c := New("http://127.0.0.1:1", testToken)
	for _, bad := range []string{"../etc/passwd", "a/b/c", "a b/c", "", "a/b?x=1"} {
		if _, err := c.GetRepo(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "invalid repository") {
			t.Errorf("GetRepo(%q) err = %v, want invalid repository", bad, err)
		}
	}
}

func TestGetRepoAndAuthErrors(t *testing.T) {
	f := newFakeGitHub(t, nil)
	r, err := New(f.api.URL, testToken).GetRepo(context.Background(), "acme/app")
	if err != nil || r.FullName != "acme/app" || !r.Private {
		t.Fatalf("GetRepo = %+v, %v", r, err)
	}
	for _, tc := range []struct {
		token  string
		repo   string
		status int
		msg    string
	}{
		{"", "acme/app", 401, "Requires authentication"},
		{"wrong", "acme/app", 401, "Bad credentials"},
		{testToken, "acme/nope", 404, "Not Found"},
	} {
		_, err := New(f.api.URL, tc.token).GetRepo(context.Background(), tc.repo)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != tc.status || ae.Message != tc.msg {
			t.Errorf("token %q repo %s: err = %v, want HTTP %d %q", tc.token, tc.repo, err, tc.status, tc.msg)
		}
	}
}

// TestOtherPrivateRepos: only PRIVATE repos other than this one count —
// public repos are readable by any fine-grained token and say nothing about
// how it was scoped.
func TestOtherPrivateRepos(t *testing.T) {
	f := newFakeGitHub(t, nil)
	n, more, err := New(f.api.URL, testToken).OtherPrivateRepos(context.Background(), "acme/app")
	if err != nil || n != 2 || more {
		t.Errorf("OtherPrivateRepos = %d, more=%v, err=%v; want 2 (acme/other, me/secret), false", n, more, err)
	}
}

// TestListSuccessfulRuns keeps push and workflow_dispatch runs of this
// repo's own code and drops pull_request runs and fork runs.
func TestListSuccessfulRuns(t *testing.T) {
	f := newFakeGitHub(t, nil)
	runs, err := New(f.api.URL, testToken).ListSuccessfulRuns(context.Background(), "acme/app", ".github/workflows/deploymate.yml", "main", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].ID != 4 || runs[1].ID != 1 {
		t.Fatalf("runs = %+v, want ids [4 1]", runs)
	}
	if runs[0].HeadSHA != "d4" || runs[0].RunNumber != 4 || runs[0].HeadCommit.Message != "newest" || runs[1].RunAttempt != 2 {
		t.Errorf("run fields lost: %+v %+v", runs[0], runs[1])
	}
}

func TestListRunArtifacts(t *testing.T) {
	f := newFakeGitHub(t, nil)
	arts, err := New(f.api.URL, testToken).ListRunArtifacts(context.Background(), "acme/app", 4)
	if err != nil || len(arts) != 2 {
		t.Fatalf("artifacts = %+v, %v", arts, err)
	}
	a := arts[1]
	if a.ID != 12 || a.Name != "deploymate-app" || a.SizeBytes != 1583 || a.Expired || a.Digest != "sha256:abc" || a.ExpiresAt.IsZero() {
		t.Errorf("artifact = %+v", a)
	}
	if arts[0].Digest != "" {
		t.Errorf("a null digest must read as empty, got %q", arts[0].Digest)
	}
}

// TestDownloadArtifactFollowsRedirectWithoutToken is the credential-safety
// contract: the API call carries the token, the 302 target (a different
// host) must NOT, the sha256 matches the bytes, and the signed URL never
// leaks into an error.
func TestDownloadArtifactFollowsRedirectWithoutToken(t *testing.T) {
	payload := bytes.Repeat([]byte("PK-zip-bytes-"), 100)
	f := newFakeGitHub(t, payload)
	var got bytes.Buffer
	n, sum, err := New(f.api.URL, testToken).DownloadArtifact(context.Background(), "acme/app", 12, &got, 1<<20)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	want := sha256.Sum256(payload)
	if n != int64(len(payload)) || sum != hex.EncodeToString(want[:]) || !bytes.Equal(got.Bytes(), payload) {
		t.Errorf("n=%d sum=%s equal=%v", n, sum, bytes.Equal(got.Bytes(), payload))
	}
	if a := f.blobAuth.Load().(string); a != "" {
		t.Errorf("the storage host received Authorization %q — the token leaked across the redirect", a)
	}

	// Direct (non-redirect) answers work too.
	got.Reset()
	if _, _, err := New(f.api.URL, testToken).DownloadArtifact(context.Background(), "acme/app", 13, &got, 1<<20); err != nil || !bytes.Equal(got.Bytes(), payload) {
		t.Errorf("direct answer: err=%v", err)
	}

	// A storage failure reports the status but never the signed URL.
	f.blob.Close()
	_, _, err = New(f.api.URL, testToken).DownloadArtifact(context.Background(), "acme/app", 12, &bytes.Buffer{}, 1<<20)
	if err == nil || strings.Contains(err.Error(), "SECRETSIGNATURE") || strings.Contains(err.Error(), f.blob.URL) {
		t.Errorf("storage error must not leak the signed URL: %v", err)
	}
}

func TestDownloadArtifactSizeCapAndErrors(t *testing.T) {
	f := newFakeGitHub(t, bytes.Repeat([]byte("x"), 5000))
	_, _, err := New(f.api.URL, testToken).DownloadArtifact(context.Background(), "acme/app", 12, &bytes.Buffer{}, 4096)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("over-cap download err = %v, want ErrTooLarge", err)
	}
	_, _, err = New(f.api.URL, "wrong").DownloadArtifact(context.Background(), "acme/app", 12, &bytes.Buffer{}, 1<<20)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 {
		t.Errorf("bad token download err = %v, want 401", err)
	}
	_, _, err = New(f.api.URL, testToken).DownloadArtifact(context.Background(), "acme/app", 999, &bytes.Buffer{}, 1<<20)
	if !errors.As(err, &ae) || ae.Status != 404 {
		t.Errorf("unknown artifact err = %v, want 404", err)
	}
}

func TestNewTrimsBaseURL(t *testing.T) {
	if c := New("http://x/", "t"); c.BaseURL != "http://x" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c := New("", "t"); c.BaseURL != DefaultBaseURL {
		t.Errorf("default BaseURL = %q", c.BaseURL)
	}
}
