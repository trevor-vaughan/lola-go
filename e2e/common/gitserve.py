"""Serve the bare repositories under a directory over smart HTTP on loopback, through
git http-backend, so the Python lola and lola-go both fetch a real git source (lola-go
accepts plain http only on loopback). Every request appends "<METHOD> <path>" to
/run/gitserve/requests.log (a tmpfs the runner mounts), so a case asserts that a step made no
request by comparing the file's line count before and after it. With --tls it serves HTTPS
with the given certificate and key instead (the test CA of e2e/common/tls.sh).

usage: gitserve.py <root> <port> [--tls <cert> <key>]"""
import http.server
import os
import ssl
import subprocess
import sys
import threading

if len(sys.argv) not in (3, 6) or len(sys.argv) == 6 and sys.argv[3] != "--tls":
    sys.exit("usage: gitserve.py <root> <port> [--tls <cert> <key>]")
ROOT = sys.argv[1]
PORT = int(sys.argv[2])
LOG = "/run/gitserve/requests.log"
LOG_LOCK = threading.Lock()


class Handler(http.server.BaseHTTPRequestHandler):
    def _cgi(self):
        path, _, query = self.path.partition("?")
        with LOG_LOCK, open(LOG, "a", encoding="utf-8") as log:
            log.write(f"{self.command} {path}\n")
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


server = http.server.ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
if len(sys.argv) == 6:
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(sys.argv[4], sys.argv[5])
    server.socket = context.wrap_socket(server.socket, server_side=True)
server.serve_forever()
