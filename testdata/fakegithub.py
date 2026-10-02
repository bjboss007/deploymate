#!/usr/bin/env python3
"""A fake GitHub for `make e2e-artifact` (docs/specs/prebuilt-deploys.md).

Mirrors the response shapes captured from real GitHub (spike S1, 2026-10-02):
Bearer auth (401 otherwise), artifact lists with a `digest` of the zip, and a
302 for the download to a SEPARATE storage host that rejects any request
carrying an Authorization header (the token must never cross the redirect).

  fakegithub.py API_PORT BLOB_PORT HELLO_JAR TOKEN OWNER/REPO

Runs by id:
  1001  good, jar v1          1006  two jars
  1002  good, jar v2          1007  no jar in the zip
  1003  artifact expired      1008  the "artifact" is not a zip
  1004  artifact name differs 1099  (unknown -> 404)
  1005  digest mismatch
GET /__stats reports how the fake was used (API calls, blob auth seen).
"""
import hashlib
import io
import json
import re
import sys
import threading
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

api_port, blob_port, hello_jar, TOKEN, REPO = int(sys.argv[1]), int(sys.argv[2]), sys.argv[3], sys.argv[4], sys.argv[5]
JAR = open(hello_jar, "rb").read()


def make_zip(files):
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        for name, data in files.items():
            z.writestr(name, data)
    return buf.getvalue()


# artifact id -> zip bytes ; run id -> list of artifact dicts
ZIPS, RUNS = {}, {}


def add(run, aid, name, data, expired=False, digest="auto"):
    ZIPS[aid] = data
    d = "sha256:" + hashlib.sha256(data).hexdigest() if digest == "auto" else digest
    RUNS.setdefault(run, []).append({"id": aid, "name": name, "size_in_bytes": len(data), "expired": expired,
                                     "created_at": "2026-10-02T11:32:55Z", "expires_at": "2026-10-03T11:32:54Z",
                                     "digest": d})


# v2 differs from v1 only by an extra file (same program, different bytes)
jar_v2 = None
with zipfile.ZipFile(io.BytesIO(JAR)) as zin:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as zout:
        for item in zin.infolist():
            zout.writestr(item, zin.read(item.filename))
        zout.writestr("META-INF/dm-e2e-version.txt", "v2\n")
    jar_v2 = buf.getvalue()

add(1001, 1, "deploymate-app", make_zip({"app.jar": JAR}))
add(1002, 2, "deploymate-app", make_zip({"app.jar": jar_v2}))
add(1003, 3, "deploymate-app", make_zip({"app.jar": JAR}), expired=True)
add(1004, 4, "some-other-artifact", make_zip({"app.jar": JAR}))
add(1005, 5, "deploymate-app", make_zip({"app.jar": JAR}), digest="sha256:" + "0" * 64)
add(1006, 6, "deploymate-app", make_zip({"a.jar": JAR, "b.jar": JAR}))
add(1007, 7, "deploymate-app", make_zip({"README.txt": b"no jar here"}))
add(1008, 8, "deploymate-app", b"this is not a zip archive", digest="auto")

stats = {"api_calls": 0, "blob_requests": 0, "blob_auth_seen": False}


class Quiet(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def send_json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class API(Quiet):
    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/__stats":
            return self.send_json(200, stats)
        stats["api_calls"] += 1
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            return self.send_json(401, {"message": "Bad credentials"})
        if path == "/repos/" + REPO:
            return self.send_json(200, {"full_name": REPO, "private": True})
        m = re.fullmatch(r"/repos/" + re.escape(REPO) + r"/actions/runs/(\d+)/artifacts", path)
        if m:
            arts = RUNS.get(int(m.group(1)))
            if arts is None:
                return self.send_json(404, {"message": "Not Found"})
            return self.send_json(200, {"total_count": len(arts), "artifacts": arts})
        m = re.fullmatch(r"/repos/" + re.escape(REPO) + r"/actions/artifacts/(\d+)/zip", path)
        if m and int(m.group(1)) in ZIPS:
            self.send_response(302)
            self.send_header("Location", "http://127.0.0.1:%d/blob/%s?sig=SECRETSIGNATURE" % (blob_port, m.group(1)))
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self.send_json(404, {"message": "Not Found"})


class Blob(Quiet):
    def do_GET(self):
        stats["blob_requests"] += 1
        if self.headers.get("Authorization"):
            stats["blob_auth_seen"] = True
            return self.send_json(400, {"message": "storage rejects Authorization"})
        aid = int(self.path.split("?")[0].rsplit("/", 1)[1])
        data = ZIPS[aid]
        self.send_response(200)
        self.send_header("Content-Type", "application/zip")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


for port, handler in ((blob_port, Blob), (api_port, API)):
    srv = ThreadingHTTPServer(("127.0.0.1", port), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
print("fake github up: api=%d blob=%d" % (api_port, blob_port), flush=True)
threading.Event().wait()
