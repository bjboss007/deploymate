# JSON API and MCP server — specification

**Status:** all three tiers (read, deploy, provision) implemented 2026-10-04 · ADR 0020 · Owner: solo

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

## Deploy tier (implemented)

`POST /api/v1/apps/{slug}/{deploy|redeploy|run-workflow|start|stop|restart}` and
`POST /api/v1/deployments/{id}/{retry|rollback}` (scope `deploy`). They share
their logic with the dashboard buttons (`deployCore`, `redeployCore`,
`retryCore`, `rollbackCore`, `runWorkflowCore`, `lifecycle`) — one rule, two
front doors, so the guards (retry only the newest failed deployment, no retry of
an expired artifact, …) apply to agents identically. Queued work answers `202`
with `deployment_id`; a business-rule refusal answers `409` with the reason.
MCP tools: `deploy_app`, `redeploy_app`, `run_workflow`, `retry_deployment`,
`rollback_deployment` (destructive hint), `restart_app`, `start_app`, `stop_app`
(destructive hint), plus the read tool `wait_for_deployment` (polls until
running/failed or a timeout, returns the explanation when it failed).

**Audit trail.** Every state-changing call — accepted or refused — is a row in
`audit_log` (migration 0021: token id + name, action, target, detail, result) and,
for app-scoped actions, an `api_action` event in the app's history ("Changed
through the API: retry … via API token “agent”"). The tokens page lists the latest
25. Lifecycle actions write the app's own started/stopped/restarted event naming
the token.

**Rate limits** (per token, in memory, `api_rate.go`): reads 300/min; writes
20/min and 200/h. Over the limit: `429` + `Retry-After`.

## Provision tier (implemented)

Scope `provision`: `POST /projects`, `POST /projects/{slug}/apps`,
`POST /projects/{slug}/services`, `POST /services/{slug}/start`,
`PUT /apps/{slug}/variables`, `POST /apps/{slug}/git`,
`POST /apps/{slug}/domains`, `PATCH /apps/{slug}/config` (image, port). MCP
tools: `create_project`, `create_app`, `create_service`, `start_service`,
`set_variables` (destructive hint: overwrites), `connect_repository`,
`add_domain`, `configure_app`. They share their rules with the dashboard forms
(`createProjectCore`, `createAppCore`, `createServiceCore`, `connectRepoCore`,
`addDomainCore`, `upsertEnvItems` in `provision.go`).

- **Variables are write-only.** Stored encrypted; names that look sensitive are
  always masked, others when listed in `secret`; the API never returns a value,
  and the audit log and app history carry names only. All or nothing: one bad
  name rejects the call and writes nothing.
- **`connect_repository`** returns only the PUBLIC deploy key; the private half,
  the webhook secret and any GitHub token stay in the dashboard. It refuses an
  app that already has a repository (replacing one would orphan its key and
  webhook — a dashboard decision).
- **Not exposed, on purpose:** prebuilt-mode setup (it needs the GitHub token, a
  secret), deleting anything, service/app resizing.
- **There is no delete anywhere in the API** (`TestAPIHasNoDeletes`; the MCP test
  asserts no tool name contains delete/remove/destroy).

## Using it

Create a token at `/settings/tokens` (start read-only), then register the server
with your MCP client, for Claude Code:

```
claude mcp add deploymate \
  -e DEPLOYMATE_URL=https://your-dashboard -e DEPLOYMATE_TOKEN=dm_… \
  -- /path/to/deploymate mcp
```

or in a `.mcp.json`:

```json
{ "mcpServers": { "deploymate": {
    "command": "/path/to/deploymate", "args": ["mcp"],
    "env": { "DEPLOYMATE_URL": "http://127.0.0.1:8090", "DEPLOYMATE_TOKEN": "dm_…" } } } }
```

The agent sees only the tools its token's scope allows. Good first prompts: "how
is everything?" (`fleet_status`), "why did erp fail?" (`get_app` →
`get_deployment_log`), then, with a deploy token, "retry it and tell me when it's
done" (`retry_deployment` → `wait_for_deployment`).

## Safety

- bearer only, no cookies on `/api`; opaque 401s; `no-store` on token display
- results capped (30 KB per tool call, logs ≤ 2000 lines, 500 chars per line)
- path arguments are escaped (`../fleet` cannot climb out of its segment)
- tools carry MCP annotations (`readOnlyHint`, `destructiveHint`) so clients can
  ask the person before running anything that changes state

