"""A stand-in for an OpenAI-compatible model, for the AI assistant e2e test: it looks up the
change kinds, proposes an uptime check, then answers. No real model or key is involved."""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length") or 0)).decode()
        done = body.count('"tool_call_id"')

        def call(i, name, args):
            return {"choices": [{"message": {"content": "", "tool_calls": [{"id": "c%d" % i, "type": "function",
                    "function": {"name": name, "arguments": json.dumps(args)}}]}}], "usage": {"prompt_tokens": 120, "completion_tokens": 20}}
        if done == 0:
            out = call(1, "list_containers", {})
        elif done == 1:
            out = call(2, "propose_change", {"kind": "uptime.set", "params": {"name": "e2e-ai", "kind": "http", "target": "https://example.com"},
                                             "why": "watch the site"})
        else:
            out = {"choices": [{"message": {"content": "我看了容器列表，建议加一个可用性检测。"}}], "usage": {"prompt_tokens": 80, "completion_tokens": 15}}
        b = json.dumps(out).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def log_message(self, *a):
        pass


HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
