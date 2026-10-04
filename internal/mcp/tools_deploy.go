package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// pollEvery is how often wait_for_deployment asks; tests shorten it.
var pollEvery = 3 * time.Second

func actionTool(name, desc, scope string, destructive bool, arg, argDesc, pathPrefix, pathSuffix string) tool {
	return tool{
		Name: name, Description: desc, Scope: scope, Destructive: destructive,
		Schema: schema(map[string]any{arg: strProp(argDesc)}, arg),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need(arg)
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodPost, pathPrefix + esc(v[0]) + pathSuffix, nil, nil
		},
	}
}

func init() {
	deployTools = []tool{
		actionTool("deploy_app",
			"Deploy an app: the latest successful CI run (prebuilt apps), a build of its repository, or its image. Returns a deployment id — follow it with wait_for_deployment.",
			store.ScopeDeploy, false, "app", "the app's slug", "/apps/", "/deploy"),
		actionTool("redeploy_app",
			"Redeploy the version that is already running so changed settings and variables take effect, with no new build.",
			store.ScopeDeploy, false, "app", "the app's slug", "/apps/", "/redeploy"),
		actionTool("run_workflow",
			"Start the app's GitHub Actions workflow (prebuilt apps) to build a fresh artifact. Needs a GitHub token with Actions read & write.",
			store.ScopeDeploy, false, "app", "the app's slug", "/apps/", "/run-workflow"),
		actionTool("retry_deployment",
			"Retry a FAILED deployment (same commit / CI run / image). Only the app's newest deployment can be retried; the reason is returned if it cannot.",
			store.ScopeDeploy, false, "deployment_id", "the failed deployment's id", "/deployments/", "/retry"),
		actionTool("rollback_deployment",
			"Roll the app back to an earlier deployment's saved image. This changes what is live.",
			store.ScopeDeploy, true, "deployment_id", "the deployment to return to", "/deployments/", "/rollback"),
		actionTool("restart_app", "Restart an app's containers (keeps their current environment; use redeploy_app to apply new variables).",
			store.ScopeDeploy, false, "app", "the app's slug", "/apps/", "/restart"),
		actionTool("start_app", "Start a stopped app.", store.ScopeDeploy, false, "app", "the app's slug", "/apps/", "/start"),
		actionTool("stop_app", "Stop an app. It stops serving until started again.", store.ScopeDeploy, true, "app", "the app's slug", "/apps/", "/stop"),
	}
	// Waiting is a read: it only watches.
	readTools = append(readTools, tool{
		Name: "wait_for_deployment", Scope: store.ScopeRead,
		Description: "Wait until a deployment finishes (running or failed) or the timeout passes, then return it — with the plain-words explanation when it failed. Use after deploy_app / retry_deployment / redeploy_app.",
		Schema: schema(map[string]any{
			"deployment_id":   strProp("the deployment id"),
			"timeout_seconds": intProp("how long to wait (5-180, default 90)"),
		}, "deployment_id"),
		Run: waitForDeployment,
	})
}

func waitForDeployment(ctx context.Context, s *Server, a args) (string, bool) {
	v, err := a.need("deployment_id")
	if err != nil {
		return err.Error(), true
	}
	timeout := time.Duration(clampSeconds(a.num("timeout_seconds", 90))) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		status, b, err := s.api(ctx, http.MethodGet, "/deployments/"+esc(v[0]), nil)
		if err != nil {
			return "could not reach DeployMate: " + err.Error(), true
		}
		if status/100 != 2 {
			return apiErrorText(status, b), true
		}
		var d struct {
			Deployment struct {
				Status string `json:"status"`
			} `json:"deployment"`
		}
		_ = json.Unmarshal(b, &d)
		if d.Deployment.Status == "running" || d.Deployment.Status == "failed" || time.Now().After(deadline) {
			text := prettyJSON(b)
			if d.Deployment.Status != "running" && d.Deployment.Status != "failed" {
				text = "Still " + d.Deployment.Status + " after " + timeout.String() + " — ask again to keep waiting.\n" + text
			}
			return text, d.Deployment.Status == "failed"
		}
		select {
		case <-ctx.Done():
			return "cancelled while waiting", true
		case <-time.After(pollEvery):
		}
	}
}

func clampSeconds(n int) int {
	if n < 5 {
		return 5
	}
	if n > 180 {
		return 180
	}
	return n
}
