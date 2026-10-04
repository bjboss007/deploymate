# JSON API and MCP server — specification

**Status:** step 1 (read tier) implemented 2026-10-04 · ADR 0020 · Owner: solo

## Goal

An AI agent (or a script) can **monitor**, **deploy** and **provision** on a
DeployMate server through the Model Context Protocol, with the platform — not
the agent's good behaviour — bounding what it can do.

## Shape

```
agent ⇄ (stdio, MCP) ⇄ `deploymate mcp` ⇄ (HTTPS, Bearer dm_…) ⇄ /api/v1 ⇄ store / worker queue
```

`deploymate mcp` (cmd/deploymate/mcp.go, internal/mcp) needs only
`DEPLOYMATE_URL` (default `http://127.0.0.1:8090`) and `DEPLOYMATE_TOKEN`. It
never opens the database, the key file or Docker. It asks `/api/v1/whoami` for
the token's scope and **advertises only the tools that scope allows**; the API
enforces the scope again, so a hand-written call gets the same answer.

## Scopes (each includes the ones before)

| Scope | Allows | Enforced by |
|---|---|---|
| `read` | monitor: every GET | `auth.RequireAPIToken` — non-GET needs ≥ deploy |
| `deploy` | + act on apps that exist | `auth.RequireScope("deploy")` on the route group |
| `provision` | + create and configure | `auth.RequireScope("provision")` |

A pre-tier `write` token ranks as `deploy`; it is never widened.

## Never available through any token

Deleting anything (apps, services, projects, volumes), reading a secret's value
(variables list names and a masked flag only), connection strings, the GitHub
token / webhook secret / deploy key private half, token management, backup
keys. These stay dashboard clicks by design.

## Read tier (implemented)

`GET /api/v1/…`: `fleet`, `projects`, `projects/{slug}`, `apps/{slug}`,
`apps/{slug}/deployments`, `apps/{slug}/activity`, `apps/{slug}/logs`,
`deployments/{id}` (with the plain-words explanation and `can_retry`),
`deployments/{id}/log`, `services/{slug}`. MCP tools: `fleet_status`,
`list_projects`, `get_project`, `get_app`, `list_deployments`,
`get_deployment`, `get_deployment_log`, `get_app_logs`, `get_app_activity`,
`get_service`. A test (`TestAPIReadEndpointsAndNoSecrets`) seeds a secret
variable, a normal variable, the GitHub token and a webhook secret and asserts
none of them appears in any response.

## Safety

- bearer only, no cookies on `/api`; opaque 401s; `no-store` on token display
- results capped (30 KB per tool call, logs ≤ 2000 lines, 500 chars per line)
- path arguments are escaped (`../fleet` cannot climb out of its segment)
- tools carry MCP annotations (`readOnlyHint`, `destructiveHint`) so clients can
  ask the person before running anything that changes state

## Next (not yet built)

Step 2 — deploy tier + audit trail + rate limits. Step 3 — provision tier.
