#!/usr/bin/env python3
"""Capture the DEMO dashboard as static HTML for the website's tour.

    DEPLOYMATE_DATA_DIR=/tmp/dm-demo deploymate seed-demo
    DEPLOYMATE_DATA_DIR=/tmp/dm-demo DEPLOYMATE_DISABLE_MONITOR=1 \
      DEPLOYMATE_ADDR=127.0.0.1:8099 deploymate serve &
    python3 site/tools/capture.py http://127.0.0.1:8099

The snapshots are the dashboard's real markup rendered by the real server on
fictional data (internal/demo), styled by the dashboard's own app.css, so what
the tour shows is exactly what ships. Only this script's rewrites make them
static: forms stop posting, links point at the other snapshots (or nowhere),
the htmx script is dropped and a banner says it is a read-only demo. Never
point it at a real instance.
"""
import http.cookiejar
import pathlib
import re
import shutil
import sys
import urllib.parse
import urllib.request

BASE = (sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8099").rstrip("/")
ROOT = pathlib.Path(__file__).resolve().parent.parent
OUT = ROOT / "tour"

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))


def get(path):
    with opener.open(BASE + path) as r:
        return r.read()


def login():
    data = urllib.parse.urlencode({"email": "demo@deploymate.dev", "password": "demo-demo-demo"}).encode()
    opener.open(urllib.request.Request(BASE + "/login", data=data)).read()
    if not any(c.name == "dm_session" for c in jar):
        sys.exit("login failed — is this the seeded demo instance?")


# path on the demo server -> snapshot file
def pages():
    inv = get("/apps/invoicer").decode()
    failed = re.search(r'href="/deployments/([0-9a-f-]+)"', inv)
    if not failed:
        sys.exit("could not find the failed deployment link on /apps/invoicer")
    return {
        "/projects": "fleet.html",
        "/projects/storefront": "project.html",
        "/apps/api": "app.html",
        "/apps/invoicer": "app-failing.html",
        "/deployments/" + failed.group(1): "deployment-failed.html",
        "/services/storefront-postgres": "service.html",
        "/settings/tokens": "tokens.html",
    }


BANNER = (
    '<div style="position:sticky;top:0;z-index:50;display:flex;gap:10px;align-items:center;justify-content:center;'
    'padding:7px 12px;font:500 13px/1.3 var(--font-body,system-ui);background:var(--accent);color:var(--accent-ink);">'
    '<span>Demo — a read-only snapshot of the real dashboard on made-up data.</span>'
    '<a href="../index.html" style="color:inherit;text-decoration:underline;">Back to the site</a></div>'
)


LOG_ERR = re.compile(r"\b(error|exception|fatal|failed|denied|refused|killed|panic)\b", re.I)


def stream_lines(path, limit=300):
    """Read a finished deployment's replayed log from its SSE stream."""
    import socket
    lines = []
    try:
        r = opener.open(BASE + path, timeout=3)
        r.fp.raw._sock.settimeout(2) if hasattr(r.fp, "raw") and hasattr(r.fp.raw, "_sock") else None
        buf = b""
        while len(lines) < limit:
            try:
                chunk = r.read1(65536)
            except (socket.timeout, TimeoutError):
                break
            if not chunk:
                break
            buf += chunk
            while b"\n\n" in buf:
                ev, buf = buf.split(b"\n\n", 1)
                name, data = "message", []
                for ln in ev.decode("utf-8", "replace").split("\n"):
                    if ln.startswith("event:"):
                        name = ln[6:].strip()
                    elif ln.startswith("data:"):
                        data.append(ln[5:].lstrip())
                if name == "log" and data:
                    lines.append("\n".join(data))
        r.close()
    except Exception as e:  # a stream that never closes ends by timeout; that is fine
        if not lines:
            print("  (no log lines:", e, ")")
    return lines


def fill_logs(html, path):
    """Put the replayed log into the page and stop the page opening a stream."""
    m = re.search(r'<div id="logs" class="([^"]*)" data-log-src="([^"]*)">\s*<span class="log-wait">[^<]*</span>', html)
    if m and path.startswith("/deployments/"):
        rows = []
        for line in stream_lines(m.group(2)):
            esc = line.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
            cls = ' class="log-err"' if LOG_ERR.search(line) else ""
            rows.append(f"<div{cls}>{esc}</div>")
        html = html.replace(m.group(0), f'<div id="logs" class="{m.group(1)}">' + "".join(rows))
    # any other panel would wait for a stream that does not exist here
    html = re.sub(r' data-log-src="[^"]*"', "", html)
    html = html.replace("Connecting — history replays automatically…", "No container is running in the demo.")
    return html


def transform(html, mapping):
    html = re.sub(r'<script[^>]*htmx[^>]*></script>', "", html)
    html = html.replace('href="/static/', 'href="static/').replace('src="/static/', 'src="static/')

    def link(m):
        target = m.group(2)
        path = target.split("#")[0].split("?")[0]
        frag = target[len(path):] if target.startswith(path) else ""
        frag = "#" + target.split("#", 1)[1] if "#" in target else ""
        if path in mapping:
            return f'{m.group(1)}"{mapping[path]}{frag}"'
        return f'{m.group(1)}"#"'

    html = re.sub(r'(href=)"(/[^"]*)"', link, html)
    html = re.sub(r'<form\b([^>]*?)\saction="[^"]*"', r'<form\1 action="#" onsubmit="return false"', html)
    html = re.sub(r'(name="csrf_token"\s+value=)"[^"]*"', r'\1""', html)
    html = re.sub(r'(<body[^>]*>)', lambda m: m.group(1) + BANNER, html, count=1)
    return html


def main():
    login()
    pg = pages()
    mapping = dict(pg)
    OUT.mkdir(parents=True, exist_ok=True)
    for path, name in pg.items():
        html = fill_logs(get(path).decode(), path)
        (OUT / name).write_text(transform(html, mapping), encoding="utf-8")
        print("captured", path, "->", name)

    # the dashboard's own assets, so the snapshots look exactly like the product
    static = OUT / "static"
    shutil.rmtree(static, ignore_errors=True)
    (static / "fonts").mkdir(parents=True)
    for f in ("app.css", "app.js", "chart.umd.min.js"):
        (static / f).write_bytes(get("/static/" + f))
    css = (static / "app.css").read_text()
    for font in sorted(set(re.findall(r"fonts/([A-Za-z0-9_.-]+\.woff2)", css))):
        (static / "fonts" / font).write_bytes(get("/static/fonts/" + font))
        print("font", font)


if __name__ == "__main__":
    main()
