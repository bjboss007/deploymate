package webhooks

import (
	"testing"
	"time"
)

// TestDeliveryCachePerSource: one event delivered to several sources' hooks
// carries one GUID — each source must process it once, none twice.
func TestDeliveryCachePerSource(t *testing.T) {
	c := NewDeliveryCache()
	if c.Seen("github", "src-dev", "guid-1") {
		t.Error("first delivery to dev must be new")
	}
	if c.Seen("github", "src-stage", "guid-1") {
		t.Error("the same GUID on another source must be new (fan-out)")
	}
	if !c.Seen("github", "src-dev", "guid-1") {
		t.Error("a retry on the same source must be a duplicate")
	}
	if !c.Seen("github", "src-stage", "guid-1") {
		t.Error("a retry on stage must be a duplicate too")
	}
	if c.Seen("gitlab", "src-dev", "guid-1") {
		t.Error("providers are keyed separately")
	}
}

func TestDeliveryCacheEmptyIDNeverDeduped(t *testing.T) {
	c := NewDeliveryCache()
	if c.Seen("github", "src", "") || c.Seen("github", "src", "") {
		t.Error("deliveries without an ID cannot be deduped and must never be reported seen")
	}
}

func TestDeliveryCacheExpires(t *testing.T) {
	c := NewDeliveryCache()
	c.Seen("github", "src", "old")
	c.items["github:src:old"] = time.Now().Add(-25 * time.Hour)
	if c.Seen("github", "src", "old") {
		t.Error("entries older than 24h must be forgotten")
	}
}

// realWorkflowRun is the shape captured from a real GitHub delivery
// (spike S1, 2026-10-02), trimmed to the fields that matter.
const realWorkflowRun = `{
 "action": "completed",
 "workflow_run": {
  "id": 37001586057, "name": "DeployMate build", "path": ".github/workflows/deploymate.yml",
  "event": "push", "status": "completed", "conclusion": "success",
  "head_branch": "main", "head_sha": "a2b23cc23e0bcc9ece1fcdfceca4ef55f59423cd",
  "run_number": 1, "run_attempt": 1, "display_title": "spike: workflow + hello source",
  "head_commit": {"message": "spike: workflow + hello source"},
  "head_repository": {"full_name": "bjboss007/dm-artifact-spike", "fork": false},
  "pull_requests": []
 },
 "repository": {"full_name": "bjboss007/dm-artifact-spike"},
 "sender": {"login": "bjboss007"}
}`

func TestParseGitHubWorkflowRun(t *testing.T) {
	got, err := ParseGitHubWorkflowRun([]byte(realWorkflowRun))
	if err != nil {
		t.Fatal(err)
	}
	want := WorkflowRun{
		Action: "completed", RunID: 37001586057, RunNumber: 1, RunAttempt: 1,
		Name: "DeployMate build", Path: ".github/workflows/deploymate.yml", Event: "push",
		Status: "completed", Conclusion: "success", HeadBranch: "main",
		HeadSHA: "a2b23cc23e0bcc9ece1fcdfceca4ef55f59423cd", HeadMessage: "spike: workflow + hello source",
		HeadRepo: "bjboss007/dm-artifact-spike", Repo: "bjboss007/dm-artifact-spike",
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseGitHubWorkflowRunRejectsOtherPayloads(t *testing.T) {
	for name, body := range map[string]string{
		"push payload": `{"ref":"refs/heads/main","after":"abc"}`,
		"no run id":    `{"action":"completed","workflow_run":{"name":"x"}}`,
		"not json":     `nope`,
		"empty":        ``,
	} {
		if _, err := ParseGitHubWorkflowRun([]byte(body)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
