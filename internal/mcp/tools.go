package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/habibmuhammad/deploymate/internal/store"
)

// tool is one MCP tool: its schema, the scope it needs, and how a call becomes
// a dashboard API request.
type tool struct {
	Name        string
	Description string
	Scope       string // minimum token scope
	Destructive bool   // hint to clients: ask the person first
	Schema      map[string]any
	// build returns the HTTP method, path and JSON body for the arguments.
	Build func(a args) (method, path string, body any, err error)
	// Run, when set, replaces Build for tools that are more than one request
	// (waiting for a deployment, say). It returns the text and whether it is an error.
	Run func(ctx context.Context, s *Server, a args) (string, bool)
}

type args map[string]any

func (a args) str(k string) string {
	v, _ := a[k].(string)
	return strings.TrimSpace(v)
}

func (a args) num(k string, def int) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// need returns the named string arguments, or an error naming the first missing.
func (a args) need(keys ...string) ([]string, error) {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = a.str(k)
		if out[i] == "" {
			return nil, fmt.Errorf("missing required argument %q", k)
		}
	}
	return out, nil
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func esc(s string) string { return url.PathEscape(s) }

func errMissing(k string) error { return fmt.Errorf("missing required argument %q", k) }

// readTools monitor the platform; every one is a GET.
var readTools = []tool{
	{
		Name: "fleet_status", Scope: store.ScopeRead,
		Description: "Start here. The state of everything: counts, which apps need attention and why, and every project with its apps and databases/caches.",
		Schema:      schema(map[string]any{}),
		Build:       func(a args) (string, string, any, error) { return http.MethodGet, "/fleet", nil, nil },
	},
	{
		Name: "list_projects", Scope: store.ScopeRead,
		Description: "List projects with how many apps and services each has.",
		Schema:      schema(map[string]any{}),
		Build:       func(a args) (string, string, any, error) { return http.MethodGet, "/projects", nil, nil },
	},
	{
		Name: "get_project", Scope: store.ScopeRead,
		Description: "One project: its apps (status, health, last deploy) and its databases/caches (and which apps use them).",
		Schema:      schema(map[string]any{"project": strProp("the project's slug")}, "project"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("project")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/projects/" + esc(v[0]), nil, nil
		},
	},
	{
		Name: "get_app", Scope: store.ScopeRead,
		Description: "Everything about one app: status, health, URL, deploy mode, connected repo, which services it receives, variable NAMES (never values), domains and certificate state, and — if its last deploy failed — a plain-words explanation.",
		Schema:      schema(map[string]any{"app": strProp("the app's slug")}, "app"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("app")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/apps/" + esc(v[0]), nil, nil
		},
	},
	{
		Name: "list_deployments", Scope: store.ScopeRead,
		Description: "An app's recent deployments, newest first.",
		Schema:      schema(map[string]any{"app": strProp("the app's slug"), "limit": intProp("how many (1-100, default 20)")}, "app"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("app")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/apps/" + esc(v[0]) + "/deployments?limit=" + strconv.Itoa(a.num("limit", 20)), nil, nil
		},
	},
	{
		Name: "get_deployment", Scope: store.ScopeRead,
		Description: "One deployment. When it failed: the error, a plain-words explanation with things to try, and whether it can be retried.",
		Schema:      schema(map[string]any{"deployment_id": strProp("the deployment id")}, "deployment_id"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("deployment_id")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/deployments/" + esc(v[0]), nil, nil
		},
	},
	{
		Name: "get_deployment_log", Scope: store.ScopeRead,
		Description: "The build and deploy log of one deployment (the last N lines).",
		Schema:      schema(map[string]any{"deployment_id": strProp("the deployment id"), "tail": intProp("lines (1-2000, default 200)")}, "deployment_id"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("deployment_id")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/deployments/" + esc(v[0]) + "/log?tail=" + strconv.Itoa(a.num("tail", 200)), nil, nil
		},
	},
	{
		Name: "get_app_logs", Scope: store.ScopeRead,
		Description: "Recent output of a running app's containers (one entry per replica), with a note when a container exited or was killed.",
		Schema:      schema(map[string]any{"app": strProp("the app's slug"), "tail": intProp("lines per replica (1-500, default 100)")}, "app"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("app")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/apps/" + esc(v[0]) + "/logs?tail=" + strconv.Itoa(a.num("tail", 100)), nil, nil
		},
	},
	{
		Name: "get_app_activity", Scope: store.ScopeRead,
		Description: "An app's history in plain words: restarts, health changes, configuration changes — including changes made through this API.",
		Schema:      schema(map[string]any{"app": strProp("the app's slug"), "limit": intProp("how many (1-200, default 30)")}, "app"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("app")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/apps/" + esc(v[0]) + "/activity?limit=" + strconv.Itoa(a.num("limit", 30)), nil, nil
		},
	},
	{
		Name: "get_service", Scope: store.ScopeRead,
		Description: "One database or cache: type, state, environment, and which apps receive its connection URL. The connection string itself is never shown.",
		Schema:      schema(map[string]any{"service": strProp("the service's slug")}, "service"),
		Build: func(a args) (string, string, any, error) {
			v, err := a.need("service")
			if err != nil {
				return "", "", nil, err
			}
			return http.MethodGet, "/services/" + esc(v[0]), nil, nil
		},
	},
}

// allTools is every tool the server knows; later tiers append to it.
func allTools() []tool {
	out := append([]tool{}, readTools...)
	out = append(out, deployTools...)
	out = append(out, provisionTools...)
	return out
}

// Later tiers (act on existing apps; create and configure) live in their own
// files and register here.
var (
	deployTools    []tool
	provisionTools []tool
)

// ---- MCP surface ------------------------------------------------------------

func (s *Server) visibleTools(ctx context.Context) []map[string]any {
	rank := store.ScopeRank(s.learnScope(ctx))
	out := []map[string]any{}
	for _, t := range allTools() {
		if store.ScopeRank(t.Scope) > rank {
			continue
		}
		readOnly := t.Scope == store.ScopeRead
		out = append(out, map[string]any{
			"name": t.Name, "description": t.Description, "inputSchema": t.Schema,
			"annotations": map[string]any{
				"readOnlyHint": readOnly, "destructiveHint": t.Destructive, "openWorldHint": false,
			},
		})
	}
	return out
}

type callParams struct {
	Name      string `json:"name"`
	Arguments args   `json:"arguments"`
}

func toolText(text string, isError bool) map[string]any {
	if len(text) > maxResultBytes {
		text = text[:maxResultBytes] + "\n…(truncated)"
	}
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": isError}
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) map[string]any {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil || p.Name == "" {
		return toolText("tools/call needs a tool name", true)
	}
	var t *tool
	for _, c := range allTools() {
		if c.Name == p.Name {
			cc := c
			t = &cc
			break
		}
	}
	if t == nil {
		return toolText("unknown tool "+p.Name, true)
	}
	if store.ScopeRank(t.Scope) > store.ScopeRank(s.learnScope(ctx)) {
		return toolText(errNotAllowed.Error()+" ("+t.Name+" needs '"+t.Scope+"')", true)
	}
	if p.Arguments == nil {
		p.Arguments = args{}
	}
	if t.Run != nil {
		text, isErr := t.Run(ctx, s, p.Arguments)
		return toolText(text, isErr)
	}
	method, path, body, err := t.Build(p.Arguments)
	if err != nil {
		return toolText(err.Error(), true)
	}
	status, b, err := s.api(ctx, method, path, body)
	if err != nil {
		return toolText("could not reach DeployMate: "+err.Error(), true)
	}
	if status/100 != 2 {
		return toolText(apiErrorText(status, b), true)
	}
	return toolText(prettyJSON(b), false)
}

func apiErrorText(status int, b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	return fmt.Sprintf("DeployMate answered %d: %s", status, msg)
}

func prettyJSON(b []byte) string {
	var pretty bytes.Buffer
	if json.Indent(&pretty, b, "", "  ") == nil {
		return pretty.String()
	}
	return string(b)
}
