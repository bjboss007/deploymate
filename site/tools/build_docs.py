#!/usr/bin/env python3
"""Build site/docs/*.html from the page bodies below.

    python3 site/tools/build_docs.py

The docs are plain static HTML (no framework): this script only wraps each body
in the shared header, sidebar and footer, so the pages stay consistent. Bodies
use [[name]] placeholders for code blocks (rendered escaped, in <pre>).
workflow.example.yml is the output of internal/githubci.Workflow for a Gradle
project — regenerate it if that generator changes.
"""
import html
import pathlib

HERE = pathlib.Path(__file__).resolve().parent
DOCS = HERE.parent / "docs"
WORKFLOW = (HERE / "workflow.example.yml").read_text()

NAV = [
    ("Start", [("quickstart.html", "Quickstart"), ("concepts.html", "How it fits together")]),
    ("Agents", [("agents.html", "AI agents (MCP)")]),
]

SUN = '<svg class="icon-sun" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>'
MOON = '<svg class="icon-moon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>'

TEMPLATE = """<!doctype html>
<html lang="en" data-theme="dark">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>@@TITLE@@ — DeployMate docs</title>
<meta name="description" content="@@DESC@@">
<meta name="theme-color" content="#0d1219">
<link rel="canonical" href="https://deploymate.link/docs/@@SLUG@@">
<link rel="icon" href="../assets/favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="../assets/site.css">
<script>try{var t=localStorage.getItem("dm-site-theme");if(t)document.documentElement.setAttribute("data-theme",t);else if(matchMedia("(prefers-color-scheme: light)").matches)document.documentElement.setAttribute("data-theme","light")}catch(e){}</script>
</head>
<body>
<a class="skip" href="#main">Skip to content</a>
<header class="top"><div class="wrap">
  <a class="brand" href="../"><span class="led" aria-hidden="true"></span>deploymate</a>
  <nav class="nav" aria-label="Main"><a href="../#how">How it works</a><a href="../#tour">Tour</a><a href="../#features">Features</a><a href="../#agents">Agents</a><a href="../#install">Install</a><a href="./" style="color:var(--text)">Docs</a></nav>
  <div class="top-right">
    <button class="icon-btn" type="button" data-theme-toggle aria-label="Switch between dark and light theme">@@MOON@@@@SUN@@</button>
    <a class="btn btn-ghost btn-sm" href="https://github.com/bjboss007/deploymate">GitHub</a>
  </div>
</div></header>
<main id="main"><div class="wrap doc">
<nav aria-label="Docs">@@NAV@@</nav>
<article>
@@BODY@@
@@PAGER@@
</article>
</div></main>
<footer><div class="wrap"><span class="brand" style="font-size:16px"><span class="led" aria-hidden="true"></span>deploymate</span><span>© 2026 Habib Muhammad · Apache-2.0</span><span class="links"><a href="../">Home</a><a href="https://github.com/bjboss007/deploymate">GitHub</a></span></div></footer>
<script src="../assets/site.js"></script>
</body>
</html>
"""


def code(text):
    return '<pre class="code">' + html.escape(text) + "</pre>"


def render(slug, title, desc, body, blocks, prev=None, nxt=None):
    for name, text in blocks.items():
        body = body.replace("[[" + name + "]]", code(text))
    nav = []
    for group, items in NAV:
        nav.append('<div class="group">' + group + "</div>")
        for href, label in items:
            cur = ' aria-current="page"' if href == slug else ""
            nav.append('<a href="' + href + '"' + cur + ">" + label + "</a>")
    pager = ""
    if prev or nxt:
        left = '<a href="%s">← %s</a>' % prev if prev else "<span></span>"
        right = '<a href="%s">%s →</a>' % nxt if nxt else "<span></span>"
        pager = '<div class="pager">' + left + right + "</div>"
    out = (TEMPLATE.replace("@@SLUG@@", "" if slug == "index.html" else slug).replace("@@TITLE@@", html.escape(title)).replace("@@DESC@@", html.escape(desc))
           .replace("@@MOON@@", MOON).replace("@@SUN@@", SUN).replace("@@NAV@@", "".join(nav))
           .replace("@@BODY@@", body).replace("@@PAGER@@", pager))
    (DOCS / slug).write_text(out, encoding="utf-8")
    print("wrote", slug)


# --------------------------------------------------------------------------- quickstart
QUICK = """
<p class="kicker">Start</p>
<h1>Quickstart</h1>
<p>From a fresh server to a running app with a database, in about fifteen minutes. You need a Linux server you control and, for HTTPS, a domain.</p>

<h2 id="requirements">What you need</h2>
<table>
<tr><th>Server</th><td>Ubuntu 24.04 LTS, amd64 or arm64, with root access.</td></tr>
<tr><th>Network</th><td>Ports 80 and 443 reachable, and an A record pointing at the server for each domain you attach.</td></tr>
<tr><th>Code</th><td>A git repository (GitHub, GitLab or Gitea), or just a container image.</td></tr>
</table>

<h2 id="install">1. Install</h2>
<p>On the server, as root:</p>
[[install]]
<p>The installer downloads the release for your CPU and <strong>verifies its SHA-256</strong> against the release's <code>checksums.txt</code> — it stops on a mismatch. Then it installs Docker, opens only SSH, 80 and 443 in the firewall, creates a <code>deploymate</code> service user, starts Traefik (the only thing listening publicly) and a systemd service. It is safe to run again.</p>
<p>Pin a version with <code>DEPLOYMATE_VERSION=v0.1.0</code>. Your email is used only for Let's Encrypt.</p>
<div class="callout"><strong id="from-source">Prefer to build it yourself?</strong>You need Go and nothing else:
[[source]]</div>

<h2 id="login">2. Create your login</h2>
[[admin]]
<p>It asks for an email and a password. There is one owner account. (The service gets its data folder from its systemd unit; a command you run by hand needs it set, as above.)</p>

<h2 id="dashboard">3. Open the dashboard</h2>
<p>The dashboard listens on <code>127.0.0.1:8080</code> on the server — not on the internet. To reach it from your laptop, tunnel in:</p>
[[tunnel]]
<p>then open <a href="http://127.0.0.1:8080">http://127.0.0.1:8080</a>. To serve the dashboard itself on a domain with HTTPS, follow the <a href="https://github.com/bjboss007/deploymate/blob/main/docs/knowledge/server-setup.md">server setup guide</a>.</p>

<h2 id="image">4. Deploy something: an image</h2>
<ol>
<li>On the home page, <strong>New project</strong>, then <strong>New app</strong> inside it.</li>
<li>On the app's page choose <strong>Deploy</strong>, enter an image such as <code>nginx:1.27-alpine</code> and the port it listens on (<code>80</code>), and deploy.</li>
<li>Watch the log. When it says <code>deployed ✓</code> the app is running; its preview address is on the app page.</li>
</ol>

<h2 id="git">5. Deploy from git</h2>
<ol>
<li>On the app's <strong>Settings → Source &amp; build</strong>, enter the repository URL and branch and press <strong>Connect repo</strong>. DeployMate generates a deploy key and a webhook secret.</li>
<li>In the repository, add the <strong>deploy key</strong> (read-only). The app's Overview shows it with a Copy button.</li>
<li>Choose how it builds: leave it for a <code>Dockerfile</code>, or pick a <strong>runtime</strong> (Node.js, Python, Go, Ruby, PHP, Java, Rust, Deno, Elixir, .NET, static) and DeployMate builds it for you.</li>
<li>Press <strong>Review &amp; deploy</strong>. You'll see the commits and files that would ship; confirm, and watch the build log.</li>
<li>For deploys on every push, add a <strong>webhook</strong> in the repository: URL <code>https://your-domain/hooks/&lt;id&gt;</code> (shown on the app page), content type JSON, the secret from the app page, the <strong>push</strong> event.</li>
</ol>

<h2 id="database">6. Add a database</h2>
<ol>
<li>On the project page choose <strong>New database or cache</strong>: PostgreSQL, MySQL or Redis. Pick an <strong>environment</strong> — apps get the services of <em>their own</em> environment, so a dev app and a dev database belong together.</li>
<li>Open the new service and press <strong>Start</strong>.</li>
<li>Redeploy the app. Apps in the same project and environment now receive the connection URL as <code>DATABASE_URL</code> (Postgres), <code>REDIS_URL</code> (Redis) or <code>MYSQL_URL</code> (MySQL). On the project page each app card shows what it receives, and <strong>Stop sending</strong> turns one off for an app that doesn't use it.</li>
</ol>

<h2 id="domain">7. Go live on your domain</h2>
<ol>
<li>Point an A record at the server.</li>
<li>On the app's page, <strong>Settings → Domains</strong>, add the hostname, then redeploy to route it.</li>
<li>Certificates start in Let's Encrypt <strong>staging</strong> mode so a mistake can't use up your real rate limit. When you're ready, set <code>DEPLOYMATE_LE_MODE=production</code> in the service's environment and restart it. The domain list shows the certificate's real state and expiry.</li>
</ol>

<div class="callout warn"><strong>Back up before you trust it</strong>Backups are opt-in. Configure a storage destination (S3, R2, MinIO) with the <code>DEPLOYMATE_BACKUP_DEST_*</code> variables, then enable them on each PostgreSQL service's page. MySQL and Redis are not backed up yet.</div>
"""

QUICK_BLOCKS = {
    "install": "curl -fsSL https://raw.githubusercontent.com/bjboss007/deploymate/main/deploy/install.sh \\\n  | sudo DEPLOYMATE_LE_EMAIL=you@example.com bash",
    "source": "git clone https://github.com/bjboss007/deploymate && cd deploymate\nmake build                                   # bin/deploymate\nGOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o deploymate-linux ./cmd/deploymate\nscp deploymate-linux deploy/ root@your-server:/tmp/   # deploy/ holds the installer files\n# on the server:\nDEPLOYMATE_LE_EMAIL=you@example.com bash /tmp/deploy/bootstrap.sh /tmp/deploymate-linux",
    "admin": "sudo -u deploymate DEPLOYMATE_DATA_DIR=/var/lib/deploymate /usr/local/bin/deploymate setup-admin",
    "tunnel": "ssh -L 8080:127.0.0.1:8080 deploymate@your-server-ip",
}

# --------------------------------------------------------------------------- concepts
CONCEPTS = """
<p class="kicker">Start</p>
<h1>How it fits together</h1>
<p>The ideas behind the dashboard, so its behaviour is never a surprise.</p>

<h2 id="deploys">Deploys, and what "zero downtime" means</h2>
<p>A deploy starts the new version <strong>next to</strong> the old one and checks that it answers its health check. Only then does traffic move, and the old container is retired. If the new one never becomes healthy, nothing changes: the previous version keeps serving, and the deploy page shows the error, a plain-words explanation, and the container's own last output. With several replicas, they are replaced one at a time, so at least one is always serving.</p>

<h2 id="modes">Two ways to build</h2>
<table>
<tr><th>Build on the server</th><td>DeployMate clones the repository (read-only deploy key), builds with your <code>Dockerfile</code> or with <strong>Railpack</strong> from a runtime you choose, and deploys the image. Simple; needs enough memory on the server for the build.</td></tr>
<tr><th id="prebuilt">Prebuilt</th><td>GitHub Actions builds your app, DeployMate downloads the result and deploys it. The server never runs the build, so a small machine can run heavy apps. Currently for Java (Gradle or Maven) projects on GitHub.</td></tr>
</table>

<h3>Setting up prebuilt mode</h3>
<ol>
<li>On the app's <strong>Settings → Git</strong>, switch the deploy mode to <strong>Prebuilt (GitHub Actions)</strong>.</li>
<li>Create a <strong>fine-grained GitHub token</strong> limited to this one repository with <strong>Actions: Read-only</strong>, and paste it into the app. It is encrypted at rest and never shown again. Press <strong>Test connection</strong>.</li>
<li>Copy the workflow the app page generates into <code>.github/workflows/deploymate.yml</code>:</li>
</ol>
[[workflow]]
<ol start="4">
<li>In the repository's webhook, tick <strong>Workflow runs</strong>. When the workflow finishes, DeployMate deploys that run's artifact. Or press <strong>Deploy latest run</strong>.</li>
</ol>
<p>The artifact is checked against GitHub's digest before it is used. Artifacts expire after a day (<code>retention-days: 1</code>), so an old run can't be deployed later — press <strong>Run workflow now</strong> to build a fresh one (that needs the token to have <em>Actions: Read and write</em>).</p>

<h2 id="services">Services and environments</h2>
<p>A <strong>project</strong> groups apps with their databases and caches. Each app and each service belongs to an <strong>environment</strong> (dev, staging or production). An app receives the connection URLs of the services in <em>its own project and environment</em>, and only those. That is what keeps a staging app away from the production database.</p>
<p>Instead of clicking, put a <code>deploymate.yml</code> in your repository:</p>
[[manifest]]
<p>Each deploy creates what's missing and reuses what's there. A <code>deploymate.staging.yml</code> (or <code>.dev</code>, <code>.production</code>) <em>replaces</em> the list for that environment. In prebuilt mode the generated workflow ships these files inside the artifact, so the same manifest works there too.</p>

<h2 id="recover">Retry, redeploy, roll back, restart</h2>
<table>
<tr><th>Retry</th><td>The last deploy failed (a flaky build, a service that was down). Runs the same commit again. Only the app's newest deploy can be retried.</td></tr>
<tr><th>Redeploy</th><td>You changed variables or settings. Restarts the <em>current</em> version with the new settings — no build, no new CI run.</td></tr>
<tr><th>Roll back</th><td>The new version is bad. Returns to a previous image; the last five are kept.</td></tr>
<tr><th>Restart</th><td>Restarts the same container. It keeps the environment it was created with, so changed variables need <strong>Redeploy</strong>.</td></tr>
</table>

<h2 id="watching">What the dashboard watches</h2>
<ul>
<li><strong>Health</strong> every 30 seconds per app, with unhealthy and recovered states on the app and the fleet board.</li>
<li><strong>Uptime</strong> probes per domain, and the real <strong>certificate state</strong> (valid until, expiring, untrusted, failed).</li>
<li><strong>Alerts</strong> to a webhook (Slack-compatible) for failed deploys, unhealthy apps, uptime changes, restarts, expiring certificates and more.</li>
<li><strong>CPU and memory</strong> every 5 seconds, and live logs.</li>
</ul>
"""

CONCEPTS_BLOCKS = {
    "workflow": WORKFLOW,
    "manifest": "services:\n  - postgres:16-alpine\n  - redis:7-alpine",
}

# --------------------------------------------------------------------------- agents
AGENTS = """
<p class="kicker">Agents</p>
<h1>AI agents (MCP)</h1>
<p>DeployMate includes a <a href="https://modelcontextprotocol.io">Model Context Protocol</a> server, so an agent such as Claude can look at your fleet, explain failures, deploy, and set up new apps. You decide what it may do by choosing a token; the server enforces it.</p>

<h2 id="setup">Set it up</h2>
<ol>
<li>In the dashboard open <strong>API tokens</strong> (the key icon). Create a token, <strong>read-only to begin with</strong>. Copy it when it appears: it is shown once, and DeployMate keeps only a fingerprint.</li>
<li>Get the <code>deploymate</code> binary on the machine where your agent runs (the macOS or Linux archive from the release). It talks to your server over HTTPS and needs no access to it beyond the token.</li>
<li>Register it. For Claude Code:</li>
</ol>
[[claude]]
<p>or, in a <code>.mcp.json</code>:</p>
[[mcpjson]]
<p>Start a new session and ask <em>"how is everything?"</em></p>

<h2 id="scopes">Three levels of trust</h2>
<table>
<tr><th>Scope</th><th>The agent can</th></tr>
<tr><td><code>read</code></td><td>See everything: the fleet, apps, deployments, build logs, container logs, history, services. Variables show names only.</td></tr>
<tr><td><code>deploy</code></td><td>Also: deploy, redeploy, retry a failed deploy, roll back, start, stop, restart, run the CI workflow.</td></tr>
<tr><td><code>provision</code></td><td>Also: create projects, apps and databases/caches, start services, set variables, connect a repository, add a domain, set an app's image and port.</td></tr>
</table>
<p>The agent only <em>sees</em> the tools its token allows, and the server refuses the rest even if one is called by hand.</p>

<h2 id="tools">The tools</h2>
<table>
<tr><th>Read</th><td><code>fleet_status</code> <code>list_projects</code> <code>get_project</code> <code>get_app</code> <code>list_deployments</code> <code>get_deployment</code> <code>get_deployment_log</code> <code>get_app_logs</code> <code>get_app_activity</code> <code>get_service</code> <code>wait_for_deployment</code></td></tr>
<tr><th>Deploy</th><td><code>deploy_app</code> <code>redeploy_app</code> <code>run_workflow</code> <code>retry_deployment</code> <code>rollback_deployment</code> <code>restart_app</code> <code>start_app</code> <code>stop_app</code></td></tr>
<tr><th>Provision</th><td><code>create_project</code> <code>create_app</code> <code>create_service</code> <code>start_service</code> <code>set_variables</code> <code>connect_repository</code> <code>add_domain</code> <code>configure_app</code></td></tr>
</table>

<h2 id="safety">What it can never do</h2>
<ul>
<li><strong>Delete anything.</strong> There is no delete in the API at all — not apps, services, projects or data.</li>
<li><strong>Read a secret.</strong> Variables are write-only: <code>set_variables</code> stores a value (encrypted), and nothing ever returns it. Connection strings, the GitHub token and the webhook secret never leave the dashboard. <code>connect_repository</code> returns only the <em>public</em> deploy key.</li>
<li><strong>Manage tokens</strong> or touch backup credentials.</li>
</ul>
<p>And everything it does is on the record: each change is listed on the API tokens page and in the affected app's history, with the name of the token that made it. Writes are rate-limited per token (20 a minute, 200 an hour), so a looping agent hits a wall, not your server.</p>

<div class="callout warn"><strong>Treat build logs as untrusted text</strong>An agent that reads logs and commit messages reads text other people wrote. Keep write scopes for agents you supervise, and prefer a read-only token for anything that runs unattended. Revoke a token at any time on the API tokens page; it stops working immediately.</div>

<h2 id="prompts">Good first prompts</h2>
<ul>
<li><em>"How is everything?"</em> — <code>fleet_status</code></li>
<li><em>"Why did the last deploy of invoicer fail?"</em> — <code>get_app</code>, <code>get_deployment_log</code></li>
<li>With a deploy token: <em>"Retry it and tell me when it's done."</em> — <code>retry_deployment</code>, <code>wait_for_deployment</code></li>
<li>With a provision token: <em>"Create a staging copy of the API with its own Postgres."</em></li>
</ul>
"""

AGENTS_BLOCKS = {
    "claude": "claude mcp add deploymate \\\n  -e DEPLOYMATE_URL=https://your-dashboard \\\n  -e DEPLOYMATE_TOKEN=dm_... \\\n  -- /path/to/deploymate mcp",
    "mcpjson": '{\n  "mcpServers": {\n    "deploymate": {\n      "command": "/path/to/deploymate",\n      "args": ["mcp"],\n      "env": { "DEPLOYMATE_URL": "https://your-dashboard", "DEPLOYMATE_TOKEN": "dm_..." }\n    }\n  }\n}',
}

# --------------------------------------------------------------------------- index
INDEX = """
<p class="kicker">Docs</p>
<h1>Documentation</h1>
<p>Short on purpose: enough to get running and to know why it behaves as it does. The full reference lives next to the code.</p>
<table>
<tr><th><a href="quickstart.html">Quickstart</a></th><td>Install, first deploy, database, domain.</td></tr>
<tr><th><a href="concepts.html">How it fits together</a></th><td>Zero-downtime deploys, build modes, services and environments, recovery.</td></tr>
<tr><th><a href="agents.html">AI agents (MCP)</a></th><td>Connect an agent with scoped, audited, delete-free access.</td></tr>
</table>
<h2>In the repository</h2>
<ul>
<li><a href="https://github.com/bjboss007/deploymate/blob/main/README.md">README</a> — features, configuration reference</li>
<li><a href="https://github.com/bjboss007/deploymate/blob/main/docs/knowledge/server-setup.md">Server setup</a> — including turning an old laptop into a server</li>
<li><a href="https://github.com/bjboss007/deploymate/tree/main/docs/specs">Specifications</a> and <a href="https://github.com/bjboss007/deploymate/tree/main/docs/decisions">decision records</a></li>
</ul>
"""


def main():
    DOCS.mkdir(parents=True, exist_ok=True)
    render("quickstart.html", "Quickstart",
           "Install DeployMate on a fresh Ubuntu server, deploy an app and a database, and put it on your domain.",
           QUICK, QUICK_BLOCKS, None, ("concepts.html", "How it fits together"))
    render("concepts.html", "How it fits together",
           "How DeployMate deploys, builds, wires services to apps, and recovers.",
           CONCEPTS, CONCEPTS_BLOCKS, ("quickstart.html", "Quickstart"), ("agents.html", "AI agents (MCP)"))
    render("agents.html", "AI agents (MCP)",
           "Connect Claude or another agent to DeployMate with scoped, audited, delete-free access.",
           AGENTS, AGENTS_BLOCKS, ("concepts.html", "How it fits together"), None)
    render("index.html", "Documentation", "DeployMate documentation: quickstart, concepts and AI agent access.",
           INDEX, {}, None, ("quickstart.html", "Quickstart"))


if __name__ == "__main__":
    main()
