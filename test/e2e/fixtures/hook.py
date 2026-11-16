# Receives webhook notifications for e2e: appends each body to /tmp/e2e-hook.log.
import http.server


class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        with open("/tmp/e2e-hook.log", "ab") as f:
            f.write(body + b"\n")
        self.send_response(200)
        self.end_headers()

    def log_message(self, *a):
        pass


http.server.HTTPServer(("127.0.0.1", 7098), H).serve_forever()
