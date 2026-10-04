package mcp

import (
	"net/http"

	"github.com/habibmuhammad/deploymate/internal/store"
)

func init() {
	envProp := strProp("dev, staging or production (default: dev for apps, production for services). Only apps and services in the same project AND environment are wired together.")
	provisionTools = []tool{
		{
			Name: "create_project", Scope: store.ScopeProvision,
			Description: "Create a project — the group that holds related apps and their databases/caches.",
			Schema:      schema(map[string]any{"name": strProp("project name")}, "name"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("name")
				if err != nil {
					return "", "", nil, err
				}
				return http.MethodPost, "/projects", map[string]string{"name": v[0]}, nil
			},
		},
		{
			Name: "create_app", Scope: store.ScopeProvision,
			Description: "Create an app in a project. It starts empty and stopped: connect a repository (connect_repository) or set an image (configure_app), then deploy_app.",
			Schema: schema(map[string]any{
				"project": strProp("the project's slug"), "name": strProp("app name"), "environment": envProp,
			}, "project", "name"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("project", "name")
				if err != nil {
					return "", "", nil, err
				}
				return http.MethodPost, "/projects/" + esc(v[0]) + "/apps", map[string]string{"name": v[1], "environment": a.str("environment")}, nil
			},
		},
		{
			Name: "create_service", Scope: store.ScopeProvision,
			Description: "Create a database or cache (postgres, mysql or redis) in a project. It is created stopped — start it with start_service. Apps in the same project and environment receive its connection URL automatically on their next deploy.",
			Schema: schema(map[string]any{
				"project": strProp("the project's slug"), "name": strProp("service name, e.g. dev-postgres"),
				"type": map[string]any{"type": "string", "enum": []string{"postgres", "mysql", "redis"}}, "environment": envProp,
			}, "project", "name", "type"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("project", "name", "type")
				if err != nil {
					return "", "", nil, err
				}
				return http.MethodPost, "/projects/" + esc(v[0]) + "/services", map[string]string{"name": v[1], "type": v[2], "environment": a.str("environment")}, nil
			},
		},
		actionTool("start_service", "Start a database/cache created with create_service. Can take up to a minute while it initialises.",
			store.ScopeProvision, false, "service", "the service's slug", "/services/", "/start"),
		{
			Name: "set_variables", Scope: store.ScopeProvision, Destructive: true, // overwrites existing values
			Description: "Set environment variables on an app (existing names are overwritten). WRITE-ONLY: values are never returned by any tool. Names that look sensitive (PASSWORD, TOKEN, KEY…) are stored masked; list others in `secret` to mask them too. Redeploy the app afterwards (redeploy_app) to apply.",
			Schema: schema(map[string]any{
				"app":       strProp("the app's slug"),
				"variables": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "NAME → value"},
				"secret":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "names to store masked"},
			}, "app", "variables"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("app")
				if err != nil {
					return "", "", nil, err
				}
				vars, _ := a["variables"].(map[string]any)
				if len(vars) == 0 {
					return "", "", nil, errMissing("variables")
				}
				return http.MethodPut, "/apps/" + esc(v[0]) + "/variables", map[string]any{"variables": vars, "secret": a["secret"]}, nil
			},
		},
		{
			Name: "connect_repository", Scope: store.ScopeProvision,
			Description: "Connect a git repository to an app. Returns the PUBLIC deploy key to add to the repository (read-only) — the webhook secret and any GitHub token stay in the dashboard. Refuses an app that already has a repository.",
			Schema: schema(map[string]any{
				"app": strProp("the app's slug"), "repository": strProp("git@github.com:you/repo.git or https://github.com/you/repo.git"),
				"provider": map[string]any{"type": "string", "enum": []string{"github", "gitlab", "gitea"}, "description": "default github"},
				"branch":   strProp("branch to deploy (default main)"),
			}, "app", "repository"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("app", "repository")
				if err != nil {
					return "", "", nil, err
				}
				return http.MethodPost, "/apps/" + esc(v[0]) + "/git", map[string]string{"repository": v[1], "provider": a.str("provider"), "branch": a.str("branch")}, nil
			},
		},
		{
			Name: "add_domain", Scope: store.ScopeProvision,
			Description: "Attach a domain name to an app. Point its DNS at the server, then redeploy to route it and issue the certificate.",
			Schema:      schema(map[string]any{"app": strProp("the app's slug"), "hostname": strProp("e.g. app.example.com")}, "app", "hostname"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("app", "hostname")
				if err != nil {
					return "", "", nil, err
				}
				return http.MethodPost, "/apps/" + esc(v[0]) + "/domains", map[string]string{"hostname": v[1]}, nil
			},
		},
		{
			Name: "configure_app", Scope: store.ScopeProvision,
			Description: "Set the container image and/or port an image-based app runs (for apps that deploy an image rather than a repository). Then deploy_app.",
			Schema: schema(map[string]any{
				"app": strProp("the app's slug"), "image": strProp("e.g. nginx:1.27 or ghcr.io/acme/web:1.2"), "port": intProp("the port the app listens on"),
			}, "app"),
			Build: func(a args) (string, string, any, error) {
				v, err := a.need("app")
				if err != nil {
					return "", "", nil, err
				}
				body := map[string]any{}
				if img := a.str("image"); img != "" {
					body["image"] = img
				}
				if _, ok := a["port"]; ok {
					body["port"] = a.num("port", 0)
				}
				return http.MethodPatch, "/apps/" + esc(v[0]) + "/config", body, nil
			},
		},
	}
}
