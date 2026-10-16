"""Serve the bare repositories under a directory over smart HTTP on loopback, through
git http-backend, so the Python lola and lola-go both fetch a real git source (lola-go
accepts plain http only on loopback)."""
import http.server
import os
import subprocess
import sys

ROOT = sys.argv[1]
PORT = int(sys.argv[2])


class Handler(http.server.BaseHTTPRequestHandler):
    def _cgi(self):
        path, _, query = self.path.partition("?")
        body = b""
        if self.command == "POST":
            body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        env = {
            "PATH": os.environ["PATH"], "GIT_PROJECT_ROOT": ROOT, "GIT_HTTP_EXPORT_ALL": "1",
            "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
            "REQUEST_METHOD": self.command, "PATH_INFO": path, "QUERY_STRING": query,
            "CONTENT_TYPE": self.headers.get("Content-Type", ""), "CONTENT_LENGTH": str(len(body)),
            "GIT_PROTOCOL": self.headers.get("Git-Protocol", ""), "REMOTE_ADDR": "127.0.0.1",
        }
        out = subprocess.run(["git", "http-backend"], input=body, env=env, capture_output=True, check=False).stdout
        head, _, payload = out.partition(b"\r\n\r\n")
        status, headers = 200, []
        for line in head.decode().split("\r\n"):
            key, _, value = line.partition(": ")
            if key.lower() == "status":
                status = int(value.split()[0])
            elif key:
                headers.append((key, value))
        self.send_response(status)
        for key, value in headers:
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    do_GET = do_POST = _cgi

    def log_message(self, *args):
        pass


http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
