# No-Dockerfile fixture: proves the runtime selection path builds and runs
# a plain Python app via Railpack (stdlib only — no pip deps needed).
import http.server
import os

PORT = int(os.environ.get("PORT", 8000))


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(f"python says: hello from {os.sys.version.split()[0]}".encode())

    def log_message(self, *args):
        pass


http.server.HTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
