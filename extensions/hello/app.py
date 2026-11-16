"""A minimal Berth extension.

Berth gives it BERTH_URL, BERTH_TOKEN (a token limited to what the installer granted) and
BERTH_CA_FILE (the panel's certificate authority). The page is shown inside the panel in a
sandboxed frame; the panel tells the frame its theme and language with postMessage.
"""
import json
import os
import ssl
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

URL = os.environ.get("BERTH_URL", "").rstrip("/")
TOKEN = os.environ.get("BERTH_TOKEN", "")
CTX = ssl.create_default_context(cafile=os.environ.get("BERTH_CA_FILE")) if os.environ.get("BERTH_CA_FILE") else ssl.create_default_context()
CTX.check_hostname = False  # the panel's certificate names its LAN addresses, not host.docker.internal
EVENTS = []


def berth(method, path, body=None):
    req = urllib.request.Request(URL + path, method=method, data=json.dumps(body).encode() if body is not None else None,
                                 headers={"Authorization": "Bearer " + TOKEN, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, context=CTX, timeout=30) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read() or b"null")


PAGE = """<!doctype html><meta charset="utf-8"><title>Hello</title>
<style>body{font:14px system-ui,sans-serif;margin:16px;color:#1f2328}body.dark{background:#0d1117;color:#e6edf3}
li{margin:2px 0}code{font-family:ui-monospace,monospace}</style>
<h1 id="h">Hello from an extension</h1><p>Containers on this host: <b id="n">…</b></p><ul id="l"></ul>
<p>Events received: <b id="e">0</b></p>
<script>
addEventListener('message', (m) => { if (m.data && m.data.type === 'berth.context') { document.body.classList.toggle('dark', m.data.theme === 'dark'); document.getElementById('h').dataset.locale = m.data.locale } })
fetch('api/containers').then(r => r.json()).then(d => { document.getElementById('n').textContent = d.items.length; for (const c of d.items) { const li = document.createElement('li'); li.textContent = c.name + ' — ' + c.state; document.getElementById('l').append(li) } })
fetch('api/events').then(r => r.json()).then(d => document.getElementById('e').textContent = d.length)
</script>"""


class H(BaseHTTPRequestHandler):
    def reply(self, code, body, ctype="application/json"):
        b = body.encode() if isinstance(body, str) else json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        if self.path in ("/", "/index.html"):
            return self.reply(200, PAGE, "text/html; charset=utf-8")
        if self.path == "/api/containers":
            code, d = berth("GET", "/api/v1/hosts/local/containers")
            return self.reply(code, {"items": [{"name": c.get("name"), "state": c.get("state")} for c in (d or {}).get("items", [])]} if code == 200 else d)
        if self.path == "/api/events":
            return self.reply(200, EVENTS[-50:])
        if self.path.startswith("/api/check"):
            # what this extension may and may not do with the scopes it was granted
            out = {}
            code, _ = berth("GET", "/api/v1/hosts/local/containers")
            out["read_containers"] = code
            code, d = berth("POST", "/api/v1/hosts/local/changes/site.delete?plan=1", {"id": 1})
            out["write_sites"] = code
            out["write_sites_error"] = (d or {}).get("error", {}).get("code") if isinstance(d, dict) else None
            name = self.path.partition("restart=")[2]
            if name:
                code, d = berth("POST", "/api/v1/hosts/local/changes/container.restart", {"name": name})
                out["restart"] = code
                out["restart_change"] = (d or {}).get("id")
            return self.reply(200, out)
        return self.reply(404, {"error": "not found"})

    def do_POST(self):
        if self.path == "/hooks/berth":
            n = int(self.headers.get("Content-Length") or 0)
            try:
                EVENTS.append(json.loads(self.rfile.read(n)))
            except ValueError:
                pass
            return self.reply(200, {"ok": True})
        return self.reply(404, {"error": "not found"})

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
