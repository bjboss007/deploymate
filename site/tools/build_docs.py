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
    ("Start", [("quickstart.html", "Quickstart"), ("connect-github.html", "Connect GitHub"), ("deploy-on-push.html", "Deploy on every push"), ("concepts.html", "How it fits together")]),
    ("Agents", [("agents.html", "AI agents (MCP)")]),
    ("Operate", [("secure-your-server.html", "Secure your server"), ("build-elsewhere.html", "Build on another machine"), ("worker-nodes.html", "Worker nodes")]),
    ("Optional", [("cloudflare-tunnel.html", "No open ports? Use a tunnel")]),
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
  <nav class="nav" aria-label="Main"><a href="../#how">How it works</a><a href="../#tour">Tour</a><a href="../#features">Features</a><a href="../#nodes">Nodes</a><a href="../#agents">Agents</a><a href="../#install">Install</a><a href="./" style="color:var(--text)">Docs</a></nav>
  <div class="top-right">
    <button class="icon-btn" type="button" data-theme-toggle aria-label="Switch between dark and light theme">@@MOON@@@@SUN@@</button>
  </div>
</div></header>
<main id="main"><div class="wrap doc">
<nav aria-label="Docs">@@NAV@@</nav>
<article>
@@BODY@@
@@PAGER@@
</article>
</div></main>
<footer><div class="wrap"><span class="brand" style="font-size:16px"><span class="led" aria-hidden="true"></span>deploymate</span><span>© 2026 Habib Muhammad</span><span class="links"><a href="../">Home</a></span></div></footer>
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
<tr><th>Server</th><td>Any machine you control — a VPS, a mini PC, a spare laptop. A recent Ubuntu LTS, amd64 or arm64, with root access.</td></tr>
<tr><th>Network</th><td>Ports 80 and 443 open to the internet, and an A record pointing at the server for each domain you attach. (Can't open ports — a home or office connection, say? There is an <a href="cloudflare-tunnel.html">optional tunnel setup</a>.)</td></tr>
<tr><th>Code</th><td>A git repository (GitHub, GitLab or Gitea), or just a container image.</td></tr>
</table>

<h2 id="install">1. Install</h2>
<p>On the server, as root:</p>
[[install]]
<p>The installer downloads the release for your CPU and <strong>verifies its SHA-256</strong> against the release's <code>checksums.txt</code> — it stops on a mismatch. Then it installs Docker, opens only SSH, 80 and 443 in the firewall, creates a <code>deploymate</code> service user, starts Traefik (the only thing listening publicly) and a systemd service. It is safe to run again.</p>
<p>Pin a version with <code>DEPLOYMATE_VERSION=v0.1.0</code>. Your email is used only for Let's Encrypt.</p>

<h2 id="login">2. Create your login</h2>
[[admin]]
<p>It asks for an email and a password. There is one owner account. (The service gets its data folder from its systemd unit; a command you run by hand needs it set, as above.)</p>

<h2 id="dashboard">3. Open the dashboard</h2>
<p>The dashboard listens on <code>127.0.0.1:8080</code> on the server — not on the internet. To reach it from your laptop, tunnel in:</p>
[[tunnel]]
<p>then open <a href="http://127.0.0.1:8080">http://127.0.0.1:8080</a>.</p>
<h3 id="dashboard-domain">Or give the dashboard a domain</h3>
<p>To open it at <code>https://dm.example.com</code> like any app, point an A record for that name at the server and tell the installer (it is safe to run again):</p>
[[dashdomain]]
<p>DeployMate then writes the route for Traefik, which gets a certificate for the name. Sign-in attempts are rate-limited (ten wrong tries from one address, then a 15-minute pause), but the dashboard is now on the internet, so use a long password. Behind a Cloudflare Tunnel you don't need this: point the tunnel at <code>localhost:8080</code> instead.</p>

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
<li>For deploys on every push on GitHub, <a href="connect-github.html">connect GitHub once</a> and pick the repository from a list. For GitLab, Gitea, or GitHub without the app, add a <strong>webhook</strong> yourself: a two-minute step, covered in <a href="deploy-on-push.html">Deploy on every push</a>.</li>
</ol>

<h2 id="database">6. Add a database</h2>
<ol>
<li>On the project page choose <strong>New database or cache</strong>: PostgreSQL, MySQL, Redis or MongoDB. Pick an <strong>environment</strong> — apps get the services of <em>their own</em> environment, so a dev app and a dev database belong together.</li>
<li>Open the new service and press <strong>Start</strong>.</li>
<li>Redeploy the app. Apps in the same project and environment now receive the connection URL as <code>DATABASE_URL</code> (Postgres), <code>REDIS_URL</code> (Redis) or <code>MYSQL_URL</code> (MySQL) or <code>MONGODB_URL</code> (MongoDB), plus <code>_HOST</code>, <code>_PORT</code>, <code>_USER</code>, <code>_PASSWORD</code> and <code>_NAME</code> variants for frameworks that take them apart. On the project page each app card shows what it receives, and <strong>Stop sending</strong> turns one off for an app that doesn't use it.</li>
</ol>

<h2 id="domain">7. Go live on your domain</h2>
<ol>
<li>Point an A record at the server.</li>
<li>On the app's page, <strong>Settings → Domains</strong>, add the hostname, then redeploy to route it.</li>
<li>Certificates start in Let's Encrypt <strong>staging</strong> mode so a mistake can't use up your real rate limit. When you're ready, set <code>DEPLOYMATE_LE_MODE=production</code> in the service's environment and restart it. The domain list shows the certificate's real state and expiry.</li>
</ol>

<h2 id="update">Updating DeployMate</h2>
<p>When a new release is out, run this on the server:</p>
[[update]]
<p>It downloads the release, checks its SHA-256 against the published <code>checksums.txt</code>, and refuses on any mismatch. It then stops the dashboard service, copies the database aside, swaps the binary, starts the new version and waits for it to answer. <strong>If the new version doesn't come up, the old binary and database are put back automatically.</strong> Your apps keep running throughout, because they are separate containers; only the dashboard is briefly unavailable. If the release pins a newer Traefik (the proxy that serves your domains), the update then replaces that container too, which takes a few seconds, and puts the old one back if the new one doesn't stay up. <code>--skip-traefik</code> leaves it alone.</p>
<p><code>sudo deploymate update --check</code> only reports whether a newer release exists. <code>--version v0.1.2</code> installs a specific version, which is also how you go back. <code>--from file.tar.gz</code> installs an archive you copied over, for a server that can't reach GitHub. The previous binary stays at <code>/usr/local/bin/deploymate.prev</code> and the pre-update database at <code>data.db.pre-update</code> in the data folder.</p>

<div class="callout warn"><strong>Back up before you trust it</strong>Backups are opt-in. Configure a storage destination (S3, R2, MinIO) with the <code>DEPLOYMATE_BACKUP_DEST_*</code> variables, then enable them on each database's page. PostgreSQL, MySQL, Redis and MongoDB are covered; restoring a Redis backup restarts it for a few seconds. See <a href="secure-your-server.html">Secure your server</a> for the rest of the safety checklist.</div>
"""

QUICK_BLOCKS = {
    "dashdomain": "curl -fsSL https://deploymate.link/install \\\n  | sudo DEPLOYMATE_DASHBOARD_HOST=dm.example.com DEPLOYMATE_LE_MODE=production \\\n       DEPLOYMATE_LE_EMAIL=you@example.com bash",
    "install": "curl -fsSL https://deploymate.link/install \\\n  | sudo DEPLOYMATE_LE_EMAIL=you@example.com bash",
    "update": "sudo deploymate update",
    "admin": "sudo -u deploymate DEPLOYMATE_DATA_DIR=/var/lib/deploymate /usr/local/bin/deploymate setup-admin",
    "tunnel": "ssh -L 8080:127.0.0.1:8080 deploymate@your-server-ip",
}


PUSH = """
<p class="kicker">Start</p>
<h1>Deploy on every push</h1>
<p>Connecting a repository lets DeployMate <em>download</em> your code. It does not make your git host <em>tell</em> DeployMate when you push. For that you add a <strong>webhook</strong> to the repository: one entry, pointing at your dashboard, once per repository. After that, every push to the app's branch builds and deploys by itself.</p>
<p>Without a webhook nothing is broken — you just deploy by pressing <strong>Deploy</strong>.</p>
<div class="callout"><strong>On GitHub? You can skip this page.</strong><a href="connect-github.html">Connect GitHub</a> sets up the repository access and the push notifications for every repository in one go. Use this page for GitLab, Gitea, or when you'd rather not create a GitHub app.</div>

<h2 id="values">1. Get the two values</h2>
<p>On the app, open <strong>Settings → Source &amp; build</strong>. Under the connected repository you'll find:</p>
<table>
<tr><th>Webhook URL</th><td>Shown as <code>https://your-host/hooks/&lt;id&gt;</code>. Replace <code>your-host</code> with the address you use for the dashboard, for example <code>https://dash.example.com/hooks/&lt;id&gt;</code>. The dashboard must be reachable from the internet at that address.</td></tr>
<tr><th>Webhook secret</th><td>A random string. It proves a delivery really came from your git host, so keep it private. <strong>Rotate secret</strong> makes a new one.</td></tr>
</table>

<h2 id="github">2. GitHub</h2>
<ol>
<li>In the repository open <strong>Settings → Webhooks → Add webhook</strong>.</li>
<li><strong>Payload URL:</strong> the webhook URL from step 1.</li>
<li><strong>Content type:</strong> <code>application/json</code>.</li>
<li><strong>Secret:</strong> the webhook secret.</li>
<li><strong>Which events:</strong> choose <em>Let me select individual events</em> and tick only <strong>Pushes</strong>. For a <a href="concepts.html#prebuilt">prebuilt</a> app tick <strong>Workflow runs</strong> instead (a push is ignored for those, because the deploy waits for the build to finish).</li>
<li>Press <strong>Add webhook</strong>. GitHub immediately sends a test; the entry should get a green tick.</li>
</ol>

<h2 id="others">GitLab and Gitea</h2>
<p><strong>GitLab:</strong> Settings → Webhooks. Put the URL in <em>URL</em> and the secret in <em>Secret token</em>, and tick <em>Push events</em>. <strong>Gitea:</strong> Settings → Webhooks → Add → Gitea, the same fields as GitHub with the content type JSON and push events.</p>

<h2 id="what">What happens on a push</h2>
<ul>
<li>Only pushes to the app's <strong>tracked branch</strong> deploy; pushes to other branches are ignored.</li>
<li>Each delivery is checked against the secret first. A wrong or missing signature is rejected before anything runs.</li>
<li>A delivery GitHub sends twice is recognised and deployed once.</li>
<li>The deployment appears in the app's history, triggered by <em>webhook</em>, with the usual build log.</li>
<li>Each app has its own webhook. If several apps are built from one repository, a push deploys only the apps whose build folder it changed (set under Settings → Source &amp; build). A skipped push shows in the app's activity as “Push skipped”. Apps built from the repository root deploy on every push, and so does a push whose file list GitHub or GitLab didn't send (very large pushes).</li>
</ul>

<h2 id="tunnel">Dashboard behind a login or a tunnel</h2>
<p>Your git host has to be able to reach <code>/hooks/…</code> without signing in. If you've put the dashboard behind <strong>Cloudflare Access</strong> (a good idea), add a second Access application for the same hostname with the path <code>hooks/*</code> and a <strong>Bypass</strong> policy for everyone. Only that path is opened; the rest of the dashboard stays locked, and each webhook is still verified by its secret. The same applies to any proxy or VPN that asks visitors to authenticate.</p>

<h2 id="trouble">If a push doesn't deploy</h2>
<p>In GitHub, the webhook's <strong>Recent Deliveries</strong> tab shows what DeployMate answered for every delivery:</p>
<table>
<tr><th>Answer</th><td>Meaning</td></tr>
<tr><th><code>queued</code> (200)</th><td>It worked: a deployment was queued. Look in the app's Deployments tab.</td></tr>
<tr><th><code>pong</code></th><td>The test ping arrived. The address and secret are right.</td></tr>
<tr><th><code>ignored: not the deploy branch</code></th><td>You pushed another branch. Check the branch on the app matches.</td></tr>
<tr><th><code>ignored: not a push event</code></th><td>The webhook sends events DeployMate doesn't use; for a normal app, tick only <em>Pushes</em>.</td></tr>
<tr><th><code>ignored: this app deploys from CI runs, not pushes</code></th><td>The app is in prebuilt mode: tick <em>Workflow runs</em> on the webhook.</td></tr>
<tr><th><code>bad signature</code> (401)</th><td>The secret in GitHub doesn't match. Copy it again from the app, or rotate it and paste the new one.</td></tr>
<tr><th>A login page, 302, 403 or a Cloudflare page</th><td>Something in front of the dashboard is blocking the request. See the section above.</td></tr>
<tr><th>404, or no response at all</th><td>Wrong address or id, or the dashboard isn't reachable from the internet. Open the URL's host in a browser to check.</td></tr>
</table>
<p>You can press <strong>Redeliver</strong> on any delivery to replay it after fixing the cause.</p>
"""

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
<p>Every database and cache has its own generated password, and DeployMate hands it to your apps in the connection URL and as separate variables (<code>REDIS_HOST</code>, <code>REDIS_PORT</code>, <code>REDIS_PASSWORD</code>, <code>DATABASE_HOST</code>, <code>DATABASE_USER</code>, <code>DATABASE_PASSWORD</code> and so on) for frameworks that take them apart; point one at another with <code>MY_SETTING=${REDIS_PASSWORD}</code>. Redis services created before DeployMate 0.5 had none; their page shows <strong>Add a password</strong>, which restarts Redis (its data is kept) and redeploys the apps that use it; <strong>Remove password</strong> undoes it for an app that can't use one. All containers run with <code>no-new-privileges</code> and without raw-socket access.</p>
<p>Instead of clicking, put a <code>deploymate.yml</code> in your repository:</p>
[[manifest]]
<p>Each deploy creates what's missing and reuses what's there. A <code>deploymate.staging.yml</code> (or <code>.dev</code>, <code>.production</code>) <em>replaces</em> the list for that environment. In prebuilt mode the generated workflow ships these files inside the artifact, so the same manifest works there too.</p>

<h2 id="folders">Persistent folders</h2>
<p>A container starts fresh on every deploy, so anything it wrote to its own disk is gone. For files that must stay (uploads, a SQLite file, downloaded models), open the app and list the folders under <strong>Persistent folders</strong>, one per line, such as <code>/data</code>. Each one is kept on a named volume that survives deploys, rollbacks and even deleting the app; it lives on the machine the project runs on. If the image already has files in that folder, they are copied into the volume the first time. With several replicas they all share the folder. Persistent folders are not part of backups yet, and they apply from the next deploy.</p>

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
<li><strong>The server itself</strong> on the <strong>Server</strong> page: CPU, memory, disk space, temperature and power (on a laptop), whether Docker and Traefik are running, and a week of history. It says in one sentence when something needs you, can alert you when a problem lasts a few minutes, and has a <strong>Clean up</strong> button that frees build cache and unused images without touching running apps, rollback versions or your data.</li>
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
<tr><td><code>read</code></td><td>See everything: the fleet, the server's health, apps, deployments, build logs, container logs, history, services. Variables show names only.</td></tr>
<tr><td><code>deploy</code></td><td>Also: deploy, redeploy, retry a failed deploy, roll back, start, stop, restart, run the CI workflow.</td></tr>
<tr><td><code>provision</code></td><td>Also: create projects, apps and databases/caches, start services, set variables, connect a repository, add a domain, set an app's image and port.</td></tr>
</table>
<p>The agent only <em>sees</em> the tools its token allows, and the server refuses the rest even if one is called by hand.</p>

<h2 id="tools">The tools</h2>
<table>
<tr><th>Read</th><td><code>fleet_status</code> <code>server_status</code> <code>security_status</code> <code>list_nodes</code> <code>list_projects</code> <code>get_project</code> <code>get_app</code> <code>list_deployments</code> <code>get_deployment</code> <code>get_deployment_log</code> <code>get_app_logs</code> <code>get_app_activity</code> <code>get_service</code> <code>wait_for_deployment</code></td></tr>
<tr><th>Deploy</th><td><code>deploy_app</code> <code>redeploy_app</code> <code>run_workflow</code> <code>retry_deployment</code> <code>rollback_deployment</code> <code>restart_app</code> <code>start_app</code> <code>stop_app</code></td></tr>
<tr><th>Provision</th><td><code>create_project</code> (optionally on a worker node) <code>create_app</code> <code>create_service</code> <code>start_service</code> <code>set_variables</code> <code>connect_repository</code> <code>add_domain</code> <code>configure_app</code> (image, port, GPU, persistent folders)</td></tr>
</table>

<h2 id="safety">What it can never do</h2>
<ul>
<li><strong>Delete anything.</strong> There is no delete in the API at all — not apps, services, projects or data.</li>
<li><strong>Read a secret.</strong> Variables are write-only: <code>set_variables</code> stores a value (encrypted), and nothing ever returns it. Connection strings, the GitHub token and the webhook secret never leave the dashboard. <code>connect_repository</code> returns only the <em>public</em> deploy key.</li>
<li><strong>Manage tokens</strong> or touch backup credentials.</li>
<li><strong>Add, drain or remove a worker node.</strong> Trusting another machine is your decision, made in the dashboard. An agent can only see nodes and place a new project on a ready one.</li>
</ul>
<p>And everything it does is on the record: each change is listed on the API tokens page and in the affected app's history, with the name of the token that made it. Writes are rate-limited per token (20 a minute, 200 an hour), so a looping agent hits a wall, not your server.</p>

<div class="callout warn"><strong>Treat build logs as untrusted text</strong>An agent that reads logs and commit messages reads text other people wrote. Keep write scopes for agents you supervise, and prefer a read-only token for anything that runs unattended. Revoke a token at any time on the API tokens page; it stops working immediately.</div>

<h2 id="prompts">Good first prompts</h2>
<ul>
<li><em>"How is everything?"</em> — <code>fleet_status</code></li>
<li><em>"Is the server okay? Is there room for a big build?"</em> — <code>server_status</code></li>
<li><em>"Which machines can I run something heavy on?"</em> — <code>list_nodes</code>. With a provision token: <em>"Create a project called Training on friend-pc."</em> — <code>create_project</code> with a node. A project's node is fixed when it is created.</li>
<li><em>"Why did the last deploy of invoicer fail?"</em> — <code>get_app</code>, <code>get_deployment_log</code></li>
<li>With a deploy token: <em>"Retry it and tell me when it's done."</em> — <code>retry_deployment</code>, <code>wait_for_deployment</code></li>
<li>With a provision token: <em>"Create a staging copy of the API with its own Postgres."</em></li>
</ul>
"""

AGENTS_BLOCKS = {
    "claude": "claude mcp add deploymate \\\n  -e DEPLOYMATE_URL=https://your-dashboard \\\n  -e DEPLOYMATE_TOKEN=dm_... \\\n  -- /path/to/deploymate mcp",
    "mcpjson": '{\n  "mcpServers": {\n    "deploymate": {\n      "command": "/path/to/deploymate",\n      "args": ["mcp"],\n      "env": { "DEPLOYMATE_URL": "https://your-dashboard", "DEPLOYMATE_TOKEN": "dm_..." }\n    }\n  }\n}',
}


TUNNEL = """
<p class="kicker">Optional</p>
<h1>No open ports? Use a tunnel</h1>
<p>DeployMate normally serves the internet directly: ports 80 and 443 open, a domain pointing at the server. That is the simplest setup, and the <a href="quickstart.html">quickstart</a> uses it. This page is for when you <strong>can't or don't want to</strong> do that — a machine at home or in an office, an internet provider that blocks the ports or shares one address between customers, or a router you can't configure.</p>
<p>A tunnel flips the direction. A small program on your server connects <em>out</em> to a provider, and visitors reach your server through it. Nothing has to be opened, forwarded or made public. Cloudflare Tunnel is one such service and has a free plan; the steps below use it. It is a convenience, not part of DeployMate.</p>

<div class="callout"><strong>What you need</strong>A free Cloudflare account and a domain whose DNS is on Cloudflare (you can register one there or move an existing one). The server needs only an ordinary outbound internet connection.</div>

<h2 id="create">1. Create the tunnel</h2>
<ol>
<li>In the Cloudflare dashboard open <strong>Zero Trust</strong> (it may be named "Cloudflare One"), then <strong>Networks → Tunnels → Create a tunnel</strong>.</li>
<li>Choose <strong>Cloudflared</strong>, give it a name, and pick the operating system (Debian/Ubuntu) and CPU of your server.</li>
<li>Cloudflare shows an install command that contains a long <strong>token</strong>. Run it on the server. Keep the token private: it lets anyone run a tunnel into your account. After a minute the tunnel shows <strong>Healthy</strong>.</li>
</ol>

<h2 id="hostnames">2. Point names at DeployMate</h2>
<p>In the tunnel's <strong>Public hostname</strong> tab, add one entry per name you want reachable:</p>
<table>
<tr><th>For</th><th>Type</th><th>URL</th><th>Notes</th></tr>
<tr><td>An app's domain, e.g. <code>app.example.com</code></td><td>HTTPS</td><td><code>localhost:443</code></td><td>Turn on <strong>No TLS Verify</strong> (see below). Also add the same domain to the app in DeployMate and redeploy it.</td></tr>
<tr><td>The dashboard, e.g. <code>dash.example.com</code></td><td>HTTP</td><td><code>localhost:8080</code></td><td>Protect it — see step 3.</td></tr>
<tr><td>SSH, e.g. <code>ssh.example.com</code></td><td>SSH</td><td><code>localhost:22</code></td><td>Optional: lets you log in from anywhere. Needs <code>cloudflared</code> on your own computer.</td></tr>
</table>
<p>Cloudflare creates the DNS records for you. Apps go to port 443 because DeployMate's proxy (Traefik) redirects plain HTTP to HTTPS; <strong>No TLS Verify</strong> tells the tunnel not to insist on a certificate the proxy hasn't obtained, since Cloudflare serves the real certificate to your visitors.</p>

<h2 id="access">3. Protect the dashboard</h2>
<p>The dashboard can deploy to and control your server, so don't leave it behind a single password. In Zero Trust open <strong>Access → Applications → Add → Self-hosted</strong>, enter the dashboard's hostname, and add a policy that allows only your email address. You then sign in twice — Cloudflare first, then DeployMate — which is what you want. Leave the Access policy <strong>off</strong> hostnames that are meant to be public.</p>
<p><strong>One exception.</strong> GitHub, GitLab and Gitea can't sign in to Access, so a webhook sent to the dashboard's address would be refused. Add a second Access application for the same hostname with the path <code>hooks/*</code> and a <strong>Bypass</strong> policy. Only that path is opened, and every webhook is still checked against its secret. See <a href="deploy-on-push.html#tunnel">Deploy on every push</a>.</p>

<h2 id="ssh">4. Optional: SSH through the tunnel</h2>
<p>With the SSH hostname in place, install <code>cloudflared</code> on your own computer and add this to <code>~/.ssh/config</code>:</p>
[[sshconfig]]
<p>then <code>ssh dm-server</code> works from anywhere. Use key-based login, and consider an Access policy on this hostname too.</p>

<h2 id="notes">Things to know</h2>
<ul>
<li><strong>Stop asking for certificates.</strong> Through a tunnel, Let's Encrypt can never reach your server to issue one, so by default Traefik keeps trying and logs a failure every few minutes (harmless: visitors still get Cloudflare's valid certificate). To stop the attempts, tell DeployMate it is behind a tunnel:
[[leoff]]
Redeploy an app for its routing to pick this up. The domain list then says <em>Served via your tunnel or proxy</em> instead of a certificate state, and no certificate alerts fire. Leave it unset (or set <code>staging</code> or <code>production</code>) on a server that is reachable from the internet.</li>
<li><strong>One hostname per domain.</strong> Each domain you attach to an app needs its own public hostname on the tunnel.</li>
<li><strong>It adds a dependency.</strong> If Cloudflare or the connector is down, so is access. The server and its apps keep running, and you can still reach it on your local network.</li>
<li><strong>It isn't the only way.</strong> A router port-forward, a VPS in front, or another tunnel product also works. DeployMate only needs requests for your domains to arrive at the server's ports 80/443.</li>
</ul>
"""

TUNNEL_BLOCKS = {
    "leoff": "sudo systemctl edit deploymate\n# in the editor, add:\n#   [Service]\n#   Environment=DEPLOYMATE_LE_MODE=off\nsudo systemctl restart deploymate",
    "sshconfig": "Host dm-server\n  HostName ssh.example.com\n  User your-user\n  ProxyCommand cloudflared access ssh --hostname %h",
}

SECURE = """
<p class="kicker">Operate</p>
<h1>Secure your server</h1>
<p>A DeployMate server is a machine on the internet that can run code for you, so it deserves a few minutes of care. This page covers what DeployMate checks for you, the one command that applies the fixes, and what the dashboard does to protect itself.</p>

<h2 id="page">The Security page</h2>
<p>In the sidebar, under <strong>Server</strong>, open <strong>Security</strong>. It reads your machine and lists what an attacker could reach: whether the firewall is on and drops by default, which ports listen on every interface (databases on a public address are critical), SSH password and root login, fail2ban, automatic updates and a pending reboot, who is in the <code>docker</code> group, containers that publish a port or run privileged, and Redis services without a password. Each finding says what it found and the exact fix. The page only reads; nothing on it changes the machine. The same report is at <code>GET /api/v1/security</code> and as the MCP tool <code>security_status</code>.</p>

<h2 id="harden">deploymate harden</h2>
<p>Run it over SSH. It prints a plan and changes nothing until you add <code>--apply</code>:</p>
[[dry]]
<p>It picks a profile on its own: a <strong>public server</strong> (ports open) or a <strong>tunnel server</strong> (<code>DEPLOYMATE_LE_MODE=off</code>, see <a href="cloudflare-tunnel.html">tunnels</a>).</p>
<table>
<tr><th>Both</th><td>Automatic security updates. SSH settings: at most 4 tries per connection, no root login, and password login off, but only when you have a key installed <em>and</em> SSH has logged you in with it before. The new SSH config is checked with <code>sshd -t</code> before it takes effect.</td></tr>
<tr><th>Public</th><td>Firewall: SSH rate-limited, 80 and 443 open, everything else denied. fail2ban for SSH.</td></tr>
<tr><th>Tunnel</th><td>Ports 80 and 443 closed (the tunnel connects out). Port 22 closed to everyone except the network you give with <code>--ssh-from</code> (it must be a network the machine is on, or <code>harden</code> refuses). No fail2ban: behind a tunnel every connection looks local, so it couldn't tell attackers apart. Protect SSH with the tunnel's access policy instead.</td></tr>
</table>
<div class="callout warn"><strong>Keep a way in</strong>Changes to the firewall and SSH start a five-minute revert timer inside systemd, so it fires even if your session drops. Open a <em>second</em> terminal and log in; if it works, run <code>sudo deploymate harden --confirm</code>. If you don't, the old settings come back by themselves. <code>sudo deploymate harden --undo</code> reverses everything later. If the server is only reachable through a tunnel, make sure you can still reach the machine another way (keyboard, or your provider's console) before closing port 22.</div>

<h2 id="redis">Databases and containers</h2>
<p>New Redis services get a password, because every container on the server's network can reach every Redis. A Redis created earlier keeps working without one; its page offers <strong>Add a password</strong>, which restarts it and redeploys the apps that use it, and <strong>Remove password</strong> for an app that can't use one. Every container DeployMate starts runs with <code>no-new-privileges</code>, a trimmed set of Linux capabilities and a process limit.</p>

<h2 id="dashboard">The dashboard itself</h2>
<ul>
<li><strong>Headers.</strong> The dashboard refuses to be framed, sets a content security policy and, over HTTPS, HSTS.</li>
<li><strong>Sign-in limits.</strong> Ten failed sign-ins from one address in 15 minutes, or thirty for one email, and further attempts are refused. Behind a Cloudflare tunnel the real visitor address is used. Every attempt is listed on the <strong>Sign-ins</strong> page in the sidebar (counts, latest attempts, a tag for an address that never signed in before), and an alert event, <em>Repeated failed dashboard sign-ins</em>, can post to your webhook.</li>
<li><strong>Sessions</strong> last 7 days. <strong>Sign out everywhere</strong> on the Sign-ins page ends all of them.</li>
<li><strong>Not yet:</strong> two-factor sign-in. Until then, put an access policy (Cloudflare Access, a VPN) in front of the dashboard.</li>
</ul>

<h2 id="rotate">After a scare: what to rotate</h2>
<ol>
<li>Sign out everywhere, and change your dashboard password.</li>
<li>Revoke API tokens you don't recognise on the Tokens page, and create new ones.</li>
<li>Rotate backup storage keys (the S3 or R2 access keys) and download a fresh backup key for each database.</li>
<li>Disconnect and reconnect GitHub, and review its installation.</li>
<li>If you use a tunnel, rotate the tunnel token in Cloudflare.</li>
<li>Check <code>~/.ssh/authorized_keys</code> for keys you didn't add, and run the Security page again.</li>
</ol>
"""

BUILDHOST = """
<p class="kicker">Operate</p>
<h1>Build on another machine</h1>
<p>Building is the heaviest thing a small server does: compilers and package installs can use more memory than the apps themselves, and a build that runs out of memory fails the deploy. DeployMate can send builds to another computer instead, yours or a friend's, over <a href="https://tailscale.com">Tailscale</a>. The finished image comes back and runs on your server as before.</p>

<div class="callout"><strong>What leaves your server</strong>The source code of the app being built, and its build output. Pick a machine whose owner you trust: it sees your code while building. Runtime secrets (environment variables) are not part of a build.</div>

<h2 id="builder">1. On the strong machine</h2>
<p>It needs Docker and Tailscale, signed in to the same tailnet as your server. Start a BuildKit daemon that listens <strong>only on its Tailscale address</strong>:</p>
[[daemon]]
<p>Note the Tailscale address it prints, such as <code>100.64.0.5</code>.</p>
<div class="callout warn"><strong>Lock the door</strong>This daemon runs with full privileges and has no password: anyone who can reach port 1234 can run code on that machine. In Tailscale's access controls, allow port 1234 from your DeployMate server only.</div>

<h2 id="server">2. On your DeployMate server</h2>
[[env]]
<p>That adds <code>DEPLOYMATE_BUILD_HOST</code> to the service. The next deploy's build log starts with <code>building on tcp://100.64.0.5:1234</code>.</p>

<h2 id="notes">Good to know</h2>
<ul>
<li><strong>If the strong machine is off, deploys still work.</strong> The build log says the host isn't reachable and the build runs on your server instead. If your server is the small one, that is the build that may run out of memory, so agree when the machine will be on.</li>
<li><strong>Repeat builds are quicker.</strong> The cache lives on the build machine.</li>
<li><strong>The image still has to travel back.</strong> Over a home connection a multi-gigabyte image (many machine-learning images) is slow; that case is better solved by running the app on the stronger machine: see <a href="worker-nodes.html">Worker nodes</a>.</li>
<li>One build host for now, and only the <code>tcp://</code> form is accepted.</li>
</ul>
"""

NODES = """
<p class="kicker">Operate</p>
<h1>Worker nodes</h1>
<p>A small server doesn't have to run everything. Add another computer, yours or a friend's, as a <strong>worker node</strong>, and run a whole project on it: its apps and its databases together. DeployMate still builds, routes and monitors from your server; the containers run on the node. Use it for something heavy (a machine-learning model, video processing) or just to get more memory than your server has.</p>

<div class="callout warn"><strong>A node sees everything that runs on it</strong>Its owner can read the code, environment variables and data of what you place there. Only add machines whose owner you trust.</div>

<h2 id="need">What you need</h2>
<table>
<tr><th>The node</th><td>Any Linux machine you can reach over SSH: a friend's PC, a mini PC, another VPS. It can have a different CPU type from your server.</td></tr>
<tr><th>A private route</th><td><a href="https://tailscale.com">Tailscale</a> on both machines (free for personal use) is the easy way: each gets a private <code>100.x.x.x</code> address that works from anywhere. A shared home or office network works too. Nothing on the node is opened to the internet.</td></tr>
<tr><th>On the node</th><td>Docker, and a user that may run it.</td></tr>
</table>

<h2 id="prepare">1. Prepare the node</h2>
<p>On the node, as a user with sudo. Skip what is already there.</p>
[[prepare]]
<p>Note the node's Tailscale address (<code>tailscale ip -4</code>). Install Tailscale on your DeployMate server the same way and sign in to the same account.</p>

<h2 id="add">2. Add it in DeployMate</h2>
<ol>
<li>Open <strong>Nodes</strong> in the sidebar and press <strong>Create this server's key</strong>. It shows a public key.</li>
<li>On the node, add that key to the user from step 1:</li>
</ol>
[[authorize]]
<ol start="3">
<li>Back in DeployMate, enter a name (say <em>friend-pc</em>) and the address <code>ssh://dmnode@100.64.0.5</code> (the user, then the node's address; add <code>:2222</code> for a different SSH port), then press <strong>Add node</strong>.</li>
</ol>
<p>DeployMate connects straight away and shows the node's CPUs, memory and Docker version, or the reason it couldn't. Reach the machine through an SSH alias or a tunnel instead of a plain address? Then also fill in <em>address for its ports</em>: the private address its apps will be reached at.</p>
<p>A node is checked every minute and shows <strong>offline</strong> after three failed checks in a row. It returns to <strong>ready</strong> by itself when it answers again.</p>

<h2 id="pin">3. Choose where a project runs</h2>
<p>Open the project and, under <strong>Where it runs</strong>, pick the node. Do this <em>before</em> adding anything: a project that already has a database or a deploy stays where it is, because its apps and databases must share one machine's network. To use a node for existing work, create a new project on it.</p>
<p>From then on, deploys, databases, logs, metrics, backups and restarts of that project work as usual and happen on the node. The preview address and your domains reach it through your server, so DNS stays pointed at the server.</p>

<h2 id="manage">Taking a node out of use</h2>
<ul>
<li><strong>Drain</strong> stops anything new from being placed on it. Use it before you need the machine yourself; what already runs there keeps running.</li>
<li><strong>Remove</strong> works only when no project uses the node. Removing a node never deletes anything on the machine.</li>
</ul>

<h2 id="problems">If it won't connect</h2>
<p>The Nodes page shows the reason. The usual ones:</p>
<table>
<tr><th>It says</th><th>What to do</th></tr>
<tr><th>Permission denied (publickey)</th><td>The key from step 2 is not in that user's <code>~/.ssh/authorized_keys</code>, or the file's permissions are too open (<code>chmod 600</code>).</td></tr>
<tr><th>Permission denied … docker</th><td>The user isn't allowed to use Docker. Run <code>sudo usermod -aG docker dmnode</code> on the node, then try again.</td></tr>
<tr><th>Connection timed out, no route to host</th><td>Tailscale is down on one of the machines, or they are signed in to different accounts. Check <code>tailscale status</code> on both.</td></tr>
<tr><th>Host key verification failed</th><td>The node was reinstalled, so its SSH identity changed. If that is expected, remove the old line for it from the <code>known_hosts</code> of the user that runs DeployMate.</td></tr>
</table>

<h2 id="gpu">GPUs</h2>
<p>An app can use the node's NVIDIA graphics cards, for example to serve a language model. This works on a <strong>Linux machine with an NVIDIA card</strong>. Docker on a Mac or Windows laptop cannot pass a GPU to containers, and AMD or Intel cards are not supported yet.</p>
<h3>1. Install the GPU support on the node</h3>
<p>Install the NVIDIA driver first (<code>nvidia-smi</code> should print your card), then:</p>
[[gpu]]
<p>The last command must print the same table as <code>nvidia-smi</code>. Then press <strong>Check</strong> on the node in DeployMate: the node's line now shows the card and its memory, such as <em>NVIDIA GeForce RTX 4090 24 GB</em>.</p>
<h3>2. Switch it on for the app</h3>
<p>Put the project on the GPU node (step 3 above), then on the app's page tick <strong>Use the machine's GPUs</strong> and press Save. It applies from the next deploy. If the node has no GPU support, DeployMate says so instead of saving; if a deploy asks for a GPU on a machine that has none, it fails with the reason and what to install.</p>
<p>An app gets <em>all</em> the GPUs on its machine. GPU memory is not shared out between apps: two apps on one card compete for its memory.</p>
<h3>Example: your own AI model server</h3>
<ol>
<li>Create a project on the GPU node and an app in it. Set its image to <code>ollama/ollama</code> and its port to <code>11434</code>, turn on the GPU, and add <code>/root/.ollama</code> under <strong>Persistent folders</strong> so downloaded models are kept.</li>
<li>Deploy, add a domain, then load a model with <code>curl https://your-domain/api/pull -d '{"model":"llama3.1"}'</code>.</li>
</ol>
<p>Persistent folders are described in <a href="concepts.html">How it fits together</a>; the short version: they live on the node, survive redeploys and are kept if you delete the app. They are not part of backups yet.</p>

<h2 id="notes">Good to know</h2>
<ul>
<li><strong>Images travel to the node.</strong> The build runs on your server (or on a <a href="build-elsewhere.html">build machine</a>) and the image is sent over. A multi-gigabyte image takes a while over a home connection. If the node has a different CPU type from where the image was built, the node downloads its own copy instead.</li>
<li><strong>Ports stay private.</strong> What an app publishes on the node is bound to the node's private address, not to its public interface.</li>
<li><strong>A drained or offline node blocks deploys</strong> to it, including redeploying something that already runs there. Nothing quietly falls back to your server.</li>
<li><strong>Not yet:</strong> moving a project between machines, picking a node by free GPU automatically, jobs that run to completion, spreading one project over several machines, and picking a node automatically.</li>
</ul>
"""

NODES_BLOCKS = {
    "prepare": "curl -fsSL https://get.docker.com | sudo sh\ncurl -fsSL https://tailscale.com/install.sh | sh\nsudo tailscale up\n\n# a user DeployMate will log in as, allowed to run Docker\nsudo adduser --disabled-password --gecos \"\" dmnode\nsudo usermod -aG docker dmnode",
    "gpu": "curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | sudo gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg\ncurl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list \\\n  | sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' \\\n  | sudo tee /etc/apt/sources.list.d/nvidia-container-toolkit.list\nsudo apt-get update && sudo apt-get install -y nvidia-container-toolkit\nsudo nvidia-ctk runtime configure --runtime=docker\nsudo systemctl restart docker\n\n# the test\ndocker run --rm --gpus all ubuntu nvidia-smi",
    "authorize": "sudo -u dmnode mkdir -p -m 700 /home/dmnode/.ssh\necho 'PASTE THE PUBLIC KEY HERE' | sudo -u dmnode tee -a /home/dmnode/.ssh/authorized_keys\nsudo chmod 600 /home/dmnode/.ssh/authorized_keys",
}

BUILDHOST_BLOCKS = {
    "daemon": "docker run -d --name dm-buildkitd --restart unless-stopped --privileged \\\n  -p \"$(tailscale ip -4):1234:1234\" moby/buildkit:latest --addr tcp://0.0.0.0:1234\ntailscale ip -4",
    "env": "sudo systemctl edit deploymate\n# in the editor, add:\n#   [Service]\n#   Environment=DEPLOYMATE_BUILD_HOST=tcp://100.64.0.5:1234\nsudo systemctl restart deploymate",
}

SECURE_BLOCKS = {
    "dry": "sudo deploymate harden                                   # public server\nsudo deploymate harden --ssh-from 192.168.1.0/24          # tunnel server\nsudo deploymate harden --apply                            # when the plan looks right",
}

# --------------------------------------------------------------------------- index
CONNECT = """
<p class="kicker">Start</p>
<h1>Connect GitHub</h1>
<p>Connect GitHub once and DeployMate can clone your repositories and deploy on every push, with <strong>no deploy keys, no webhook URLs to paste and no access tokens</strong> to create or renew. You pick repositories from a list.</p>
<p>It works by creating a small <strong>private GitHub app</strong> on your own GitHub account. The app belongs to you, only sees the repositories you give it, and you can remove it at any time. The older manual setup (a deploy key plus a webhook per repository, <a href="deploy-on-push.html">covered here</a>) keeps working next to it, and is still the way for GitLab and Gitea.</p>

<div class="callout"><strong>What you need</strong>A DeployMate you can sign in to, and an address for it that GitHub can reach from the internet (a domain, or your tunnel's hostname), because GitHub sends push notifications to <code>https://your-address/hooks/github-app</code>. Open the dashboard at that public address when you connect.</div>

<h2 id="connect">1. Connect</h2>
<ol>
<li>In the dashboard, click the <strong>GitHub</strong> icon in the top bar, then <strong>Connect GitHub</strong>. Choose your personal account, or an organisation (type its name).</li>
<li>GitHub opens a page titled <em>Register new GitHub App</em>, already filled in. The name is <code>DeployMate</code> plus your address; you can change it. Press <strong>Create GitHub App</strong>.</li>
<li>You land back on DeployMate's GitHub page, which now says <strong>Connected</strong> and shows the app and its webhook address.</li>
</ol>

<h2 id="install">2. Install it on your repositories</h2>
<ol>
<li>Press <strong>Install on GitHub</strong>.</li>
<li>Choose <strong>Only select repositories</strong> and tick the ones you want to deploy (or <em>All repositories</em> if you prefer). You can change this later on GitHub.</li>
<li>Back on DeployMate's GitHub page, <strong>Where it is installed</strong> lists your account.</li>
</ol>

<h2 id="repo">3. Connect a repository to an app</h2>
<ol>
<li>Open the app, then <strong>Settings → Source &amp; build → Git</strong>.</li>
<li>Under <strong>From GitHub</strong>, pick the repository. Leave <strong>Branch</strong> empty to use the repository's default branch.</li>
<li>Press <strong>Connect repository</strong>, then <strong>Review &amp; deploy</strong>.</li>
</ol>
<p>There is nothing to copy: the app shows <em>Through your GitHub app</em> instead of a key and a webhook.</p>

<h2 id="push">4. Push</h2>
<p>From now on, every push to the app's branch deploys by itself. It appears in the app's history, triggered by <em>webhook</em>, with the usual build log. If several apps are built from one repository, a push only deploys the apps whose <strong>build folder</strong> it changed; the others show <em>Push skipped</em> in their activity.</p>
<p><strong>Prebuilt apps</strong> (a GitHub Actions workflow builds the JAR) work the same way and need no token either: switch the app to <em>Prebuilt</em> under the same panel. DeployMate reads the workflow's runs and downloads the artifact through the app, and can start the workflow with <strong>Run workflow now</strong>.</p>

<h2 id="access">What the app can and can't do</h2>
<table>
<tr><th>Read your code</th><td>Permission <em>Contents: read</em>, only for the repositories you installed it on. DeployMate clones with a short-lived token (about an hour) that GitHub issues on demand, limited to the one repository being cloned and to reading its contents. It is never written to disk.</td></tr>
<tr><th>Read CI runs, download artifacts, start a workflow</th><td>Permission <em>Actions: read and write</em>. Used by prebuilt apps only, with a token limited to that one repository and to Actions.</td></tr>
<tr><th>Hear about pushes and finished runs</th><td>Subscribed to the <em>push</em> and <em>workflow run</em> events, sent to your DeployMate's webhook address and checked against a secret before anything runs.</td></tr>
<tr><th>Change your code</th><td><strong>No.</strong> It has no write access to contents, issues, pull requests or settings.</td></tr>
<tr><th>See other accounts or repositories</th><td>No. Only what you installed it on.</td></tr>
</table>
<p>The app's private key is stored encrypted on your server and is never shown on any page. <strong>Disconnect</strong> on the GitHub page deletes it; to remove the app from GitHub as well, delete it under <em>Settings → Developer settings → GitHub Apps</em>. Apps already connected keep their code but stop deploying on push.</p>

<h2 id="tunnel">Behind a login or a tunnel</h2>
<p>GitHub has to reach <code>/hooks/github-app</code> without signing in. If the dashboard is behind <strong>Cloudflare Access</strong>, the <code>hooks/*</code> bypass from <a href="deploy-on-push.html#tunnel">Deploy on every push</a> already covers it. Everything else stays locked.</p>
<p>If you connected from an address GitHub can't reach (for example <code>http://127.0.0.1:8080</code> through an SSH tunnel), the GitHub page warns you and the app is created with its webhook <strong>switched off</strong>: cloning and the repository list work, but pushes won't deploy. To fix it, open <em>Settings → Developer settings → GitHub Apps → your app</em> on GitHub, tick <strong>Active</strong>, and set the webhook URL to <code>https://your-address/hooks/github-app</code> (leave the secret as it is).</p>

<h2 id="trouble">If something doesn't work</h2>
<table>
<tr><th>The setup page says GitHub didn't accept it</th><td>The one-time code from GitHub was used or is older than an hour. Press <strong>Connect GitHub</strong> again.</td></tr>
<tr><th>The repository list is empty</th><td>The app isn't installed on any repository yet: press <strong>Install on GitHub</strong> and select some. A repository you add on GitHub shows up within a minute.</td></tr>
<tr><th>A deploy fails with “no longer lets DeployMate's app reach this repository”</th><td>The app was removed from that repository (or deleted on GitHub). Install it again, or connect the app to a different repository.</td></tr>
<tr><th>You renamed or moved the repository</th><td>Nothing to do: DeployMate recognises the repository by GitHub's own id, so pushes keep deploying and the app picks up the new name on the next push.</td></tr>
<tr><th>A push doesn't deploy</th><td>On GitHub open <em>Settings → Developer settings → GitHub Apps → your app → Advanced</em>. <strong>Recent Deliveries</strong> shows what DeployMate answered: <code>queued</code> worked; <code>ignored: not the deploy branch</code> means you pushed another branch; <code>ignored: no app uses this repository</code> means no app is connected to it; <code>ignored: no changes in the build folder</code> means the push didn't touch the app's folder; <code>bad signature</code> means DeployMate and GitHub disagree about the secret, so disconnect and connect again. A login page, 302 or 403 means something in front of the dashboard is blocking GitHub.</td></tr>
<tr><th>The webhook says it is switched off</th><td>See <em>Behind a login or a tunnel</em> above.</td></tr>
</table>
"""
INDEX = """
<p class="kicker">Docs</p>
<h1>Documentation</h1>
<p>Short on purpose: enough to get running and to know why it behaves as it does.</p>
<table>
<tr><th><a href="quickstart.html">Quickstart</a></th><td>Install, first deploy, database, domain.</td></tr>
<tr><th><a href="connect-github.html">Connect GitHub</a></th><td>Connect once, pick repositories from a list, and every push deploys. No keys or tokens.</td></tr>
<tr><th><a href="deploy-on-push.html">Deploy on every push</a></th><td>The manual way: add a webhook yourself. For GitLab, Gitea, or GitHub without the app.</td></tr>
<tr><th><a href="concepts.html">How it fits together</a></th><td>Zero-downtime deploys, build modes, services and environments, recovery.</td></tr>
<tr><th><a href="agents.html">AI agents (MCP)</a></th><td>Connect an agent with scoped, audited, delete-free access.</td></tr>
<tr><th><a href="secure-your-server.html">Secure your server</a></th><td>The Security page, <code>deploymate harden</code>, dashboard protection, and what to rotate after a scare.</td></tr>
<tr><th><a href="build-elsewhere.html">Build on another machine</a></th><td>Let a stronger computer (yours or a friend's) do the building, over Tailscale, so a small server never runs out of memory.</td></tr>
<tr><th><a href="worker-nodes.html">Worker nodes</a></th><td>Add a friend's or your own stronger computer as a worker node and run a whole project there, over SSH and Tailscale.</td></tr>
<tr><th><a href="cloudflare-tunnel.html">No open ports? Use a tunnel</a></th><td>Optional: run DeployMate on a home or office machine through a Cloudflare Tunnel.</td></tr>
</table>
"""


def main():
    DOCS.mkdir(parents=True, exist_ok=True)
    render("quickstart.html", "Quickstart",
           "Install DeployMate on a fresh Ubuntu server, deploy an app and a database, and put it on your domain.",
           QUICK, QUICK_BLOCKS, None, ("connect-github.html", "Connect GitHub"))
    render("connect-github.html", "Connect GitHub",
           "Connect GitHub once to clone your repositories and deploy on every push, with no deploy keys, webhook URLs or access tokens.",
           CONNECT, {}, ("quickstart.html", "Quickstart"), ("deploy-on-push.html", "Deploy on every push"))
    render("deploy-on-push.html", "Deploy on every push",
           "Add a webhook so every push to your branch deploys automatically: GitHub, GitLab, Gitea, and what to do behind a login or tunnel.",
           PUSH, {}, ("connect-github.html", "Connect GitHub"), ("concepts.html", "How it fits together"))
    render("concepts.html", "How it fits together",
           "How DeployMate deploys, builds, wires services to apps, and recovers.",
           CONCEPTS, CONCEPTS_BLOCKS, ("deploy-on-push.html", "Deploy on every push"), ("agents.html", "AI agents (MCP)"))
    render("agents.html", "AI agents (MCP)",
           "Connect Claude or another agent to DeployMate with scoped, audited, delete-free access.",
           AGENTS, AGENTS_BLOCKS, ("concepts.html", "How it fits together"), ("secure-your-server.html", "Secure your server"))
    render("secure-your-server.html", "Secure your server",
           "What DeployMate checks and hardens on your server: the Security page, deploymate harden, the dashboard's protections, and what to rotate after a scare.",
           SECURE, SECURE_BLOCKS, ("agents.html", "AI agents (MCP)"), ("build-elsewhere.html", "Build on another machine"))
    render("build-elsewhere.html", "Build on another machine",
           "Let a stronger computer do your builds over Tailscale, so a small server never runs out of memory while building.",
           BUILDHOST, BUILDHOST_BLOCKS, ("secure-your-server.html", "Secure your server"), ("worker-nodes.html", "Worker nodes"))
    render("worker-nodes.html", "Worker nodes: run a project on another machine",
           "Add another computer as a worker node and run a whole project on it, over SSH on a private network such as Tailscale.",
           NODES, NODES_BLOCKS, ("build-elsewhere.html", "Build on another machine"), ("cloudflare-tunnel.html", "No open ports? Use a tunnel"))
    render("cloudflare-tunnel.html", "No open ports? Use a tunnel",
           "An optional way to run DeployMate on a machine that can't open ports 80 and 443, using a Cloudflare Tunnel.",
           TUNNEL, TUNNEL_BLOCKS, ("worker-nodes.html", "Worker nodes"), None)
    render("index.html", "Documentation", "DeployMate documentation: quickstart, concepts and AI agent access.",
           INDEX, {}, None, ("quickstart.html", "Quickstart"))


if __name__ == "__main__":
    main()
